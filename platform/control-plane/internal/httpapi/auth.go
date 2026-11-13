package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

const (
	sessionCookie  = "lore_session"
	stateCookie    = "lore_oidc_state"
	nonceCookie    = "lore_oidc_nonce"
	verifierCookie = "lore_oidc_verifier"
)

type AuthConfig struct {
	Issuer                string
	InternalIssuer        string
	ClientID              string
	ClientSecret          string
	RedirectURL           string
	PostLogoutRedirectURL string
	SecureCookies         bool
	SessionTTL            time.Duration
	BootstrapAdmin        string
	// Directory reads Active Directory accounts; nil when LDAP isn't configured.
	Directory DirectoryAccounts
	// DirectoryLinks finds an account's directory link when the token lacks ldap_id.
	DirectoryLinks DirectoryLinks
	// DirectoryRefresh is how old a directory account's groups may get before they're
	// read again; DirectoryOutageGrace how long they're still trusted while LDAP is down.
	DirectoryRefresh     time.Duration
	DirectoryOutageGrace time.Duration
	// Audience must appear in every bearer token; NativeClientID and ServiceClientID are
	// the only clients whose tokens are accepted as bearer tokens.
	Audience        string
	NativeClientID  string
	ServiceClientID string
}

type Authenticator struct {
	store              store.Store
	config             AuthConfig
	provider           *oidc.Provider
	verifier           *oidc.IDTokenVerifier
	serviceVerifier    *oidc.IDTokenVerifier
	logoutVerifier     *oidc.IDTokenVerifier
	tokenExchange      *TokenExchange
	oauth              oauth2.Config
	endSessionEndpoint string
	// directoryAttempts remembers when each account's directory state was last requested.
	directoryAttempts sync.Map
}

type sessionContext struct {
	session model.Session
	user    model.User
}

type contextKey int

const userContextKey contextKey = 1

func NewAuthenticator(ctx context.Context, store store.Store, config AuthConfig) (*Authenticator, error) {
	auth := &Authenticator{store: store, config: config}
	if config.Issuer == "" {
		return auth, nil
	}
	discoveryIssuer := config.Issuer
	if config.InternalIssuer != "" {
		discoveryIssuer = config.InternalIssuer
		ctx = oidc.InsecureIssuerURLContext(ctx, config.Issuer)
	}
	provider, err := oidc.NewProvider(ctx, discoveryIssuer)
	if err != nil {
		return nil, err
	}
	auth.provider = provider
	var endpoints struct {
		EndSession string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&endpoints); err != nil {
		return nil, err
	}
	auth.endSessionEndpoint = endpoints.EndSession
	audience := config.Audience
	if audience == "" {
		audience = defaultAudience
	}
	auth.verifier = provider.Verifier(&oidc.Config{ClientID: config.ClientID})
	auth.serviceVerifier = provider.Verifier(&oidc.Config{ClientID: audience})
	// Logout accepts expired tokens: a saved sign-in is usually removed long after its
	// last access token expired. Audience and client are still checked.
	auth.logoutVerifier = provider.Verifier(&oidc.Config{ClientID: audience, SkipExpiryCheck: true})
	endpoint := provider.Endpoint()
	if config.InternalIssuer != "" {
		keys := oidc.NewRemoteKeySet(ctx, config.InternalIssuer+"/protocol/openid-connect/certs")
		auth.verifier = oidc.NewVerifier(config.Issuer, keys, &oidc.Config{ClientID: config.ClientID})
		auth.serviceVerifier = oidc.NewVerifier(config.Issuer, keys, &oidc.Config{ClientID: audience})
		auth.logoutVerifier = oidc.NewVerifier(config.Issuer, keys, &oidc.Config{ClientID: audience, SkipExpiryCheck: true})
		endpoint.TokenURL = config.InternalIssuer + "/protocol/openid-connect/token"
	}
	auth.oauth = oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		RedirectURL:  config.RedirectURL,
		Endpoint:     endpoint,
		// No "groups" scope: the realm maps groups on the client itself, and asking for a
		// scope the realm doesn't define fails the sign-in with invalid_scope.
		Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
	}
	return auth, nil
}

func (a *Authenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		idHash := hashToken(cookie.Value)
		session, err := a.store.GetSession(r.Context(), idHash, time.Now().UTC())
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				clearCookie(w, sessionCookie, a.config.SecureCookies)
				next.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusInternalServerError, "session_unavailable", "Session lookup failed")
			return
		}
		user, err := a.store.GetUser(r.Context(), session.UserID)
		if err != nil || !user.Active() {
			clearCookie(w, sessionCookie, a.config.SecureCookies)
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/web/v1/") {
			user, err = a.refresh(r.Context(), user)
			if err != nil {
				// The directory is down past the grace period: serve the request as a
				// guest rather than with groups that may have been revoked. The session
				// stays, so access returns as soon as the directory answers again.
				next.ServeHTTP(w, r)
				return
			}
			if !user.Active() {
				clearCookie(w, sessionCookie, a.config.SecureCookies)
				next.ServeHTTP(w, r)
				return
			}
		}
		ctx := context.WithValue(r.Context(), userContextKey, sessionContext{session: session, user: user})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func currentUser(ctx context.Context) (*model.User, *model.Session) {
	value, ok := ctx.Value(userContextKey).(sessionContext)
	if !ok {
		return nil, nil
	}
	user := value.user
	session := value.session
	return &user, &session
}

func (a *Authenticator) login(w http.ResponseWriter, r *http.Request) {
	if a.provider == nil {
		writeError(w, http.StatusServiceUnavailable, "oidc_not_configured", "OIDC isn't configured")
		return
	}
	state, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random_failed", "Couldn't start login")
		return
	}
	nonce, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random_failed", "Couldn't start login")
		return
	}
	verifier, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random_failed", "Couldn't start login")
		return
	}
	setCookie(w, stateCookie, state, 600, a.config.SecureCookies)
	setCookie(w, nonceCookie, nonce, 600, a.config.SecureCookies)
	setCookie(w, verifierCookie, verifier, 600, a.config.SecureCookies)
	http.Redirect(w, r, a.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (a *Authenticator) callback(w http.ResponseWriter, r *http.Request) {
	if a.provider == nil {
		writeError(w, http.StatusServiceUnavailable, "oidc_not_configured", "OIDC isn't configured")
		return
	}
	state, err := r.Cookie(stateCookie)
	if err != nil || state.Value == "" || r.URL.Query().Get("state") != state.Value {
		writeError(w, http.StatusBadRequest, "invalid_state", "Login state is invalid")
		return
	}
	nonce, err := r.Cookie(nonceCookie)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_nonce", "Login nonce is missing")
		return
	}
	verifier, err := r.Cookie(verifierCookie)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_verifier", "Login verifier is missing")
		return
	}
	token, err := a.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(verifier.Value))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "exchange_failed", "OIDC token exchange failed")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		writeError(w, http.StatusUnauthorized, "id_token_missing", "OIDC response has no ID token")
		return
	}
	idToken, err := a.verifier.Verify(r.Context(), rawIDToken)
	if err != nil || idToken.Nonce != nonce.Value {
		writeError(w, http.StatusUnauthorized, "id_token_invalid", "OIDC ID token is invalid")
		return
	}
	var claims identityClaims
	if err := idToken.Claims(&claims); err != nil {
		writeError(w, http.StatusUnauthorized, "claims_invalid", "OIDC claims are invalid")
		return
	}
	claims.Subject = idToken.Subject
	user, err := a.signIn(r.Context(), claims)
	if errors.Is(err, ErrDirectoryUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "directory_unavailable", "Active Directory is unavailable. Try again later.")
		return
	}
	if err != nil {
		log.Printf("OIDC user sync failed: %v", err)
		writeError(w, http.StatusInternalServerError, "user_sync_failed", "Couldn't synchronize the account")
		return
	}
	if !user.Active() {
		writeError(w, http.StatusForbidden, "account_disabled", "Account is disabled")
		return
	}
	if err := a.createSession(r.Context(), w, user.ID, rawIDToken); err != nil {
		writeError(w, http.StatusInternalServerError, "session_failed", "Couldn't create a session")
		return
	}
	clearCookie(w, stateCookie, a.config.SecureCookies)
	clearCookie(w, nonceCookie, a.config.SecureCookies)
	clearCookie(w, verifierCookie, a.config.SecureCookies)
	http.Redirect(w, r, "/", http.StatusFound)
}

// LoginTokens authenticates only tokens that the identity provider issued at sign-in.
// The token exchange must use it for subject tokens: accepting the exchange's own
// repository-scoped tokens there would let a five-minute token for one repository be
// renewed forever and traded for tokens to every other repository of the same user.
func (a *Authenticator) LoginTokens() BearerAuthenticator {
	return loginTokens{authenticator: a}
}

type loginTokens struct{ authenticator *Authenticator }

func (l loginTokens) AuthenticateBearer(ctx context.Context, token string) (model.User, error) {
	return l.authenticator.authenticateLogin(ctx, token)
}

// AuthenticateBearer accepts a login token or a repository-scoped token that the token
// exchange issued. It serves the Lore server's calls on behalf of a user; it must never
// authenticate a subject token for the exchange itself, see LoginTokens.
func (a *Authenticator) AuthenticateBearer(ctx context.Context, token string) (model.User, error) {
	user, err := a.authenticateLogin(ctx, token)
	if err == nil || !errors.Is(err, errNotLoginToken) {
		return user, err
	}
	if a.tokenExchange != nil {
		user, err := a.tokenExchange.authenticate(ctx, token)
		if err != nil {
			return model.User{}, err
		}
		if user, err = a.refresh(ctx, user); err != nil {
			return model.User{}, err
		}
		if !user.Active() {
			return model.User{}, ErrAccountInactive
		}
		return user, nil
	}
	return model.User{}, errors.New("bearer token is invalid")
}

func (a *Authenticator) AuthenticateBearerForLogout(ctx context.Context, token string) (string, error) {
	if a.logoutVerifier == nil {
		return "", errors.New("logout token verifier isn't configured")
	}
	idToken, err := a.logoutVerifier.Verify(ctx, token)
	if err != nil || idToken.Subject == "" {
		return "", errors.New("logout token is invalid")
	}
	var claims identityClaims
	if err := idToken.Claims(&claims); err != nil || claims.AuthorizedParty != a.bearerClients()[0] {
		return "", errors.New("logout token wasn't issued to the native client")
	}
	return idToken.Subject, nil
}

func (a *Authenticator) SetTokenExchange(exchange *TokenExchange) {
	a.tokenExchange = exchange
}

func (a *Authenticator) devLogin(w http.ResponseWriter, r *http.Request, user model.User) {
	stored, err := a.store.UpsertUser(r.Context(), user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "user_sync_failed", "Couldn't synchronize the account")
		return
	}
	if err := a.createSession(r.Context(), w, stored.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "session_failed", "Couldn't create a session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (a *Authenticator) createSession(ctx context.Context, w http.ResponseWriter, userID string, idTokens ...string) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	csrf, err := randomToken()
	if err != nil {
		return err
	}
	ttl := a.config.SessionTTL
	if ttl <= 0 {
		ttl = 8 * time.Hour
	}
	idToken := ""
	if len(idTokens) > 0 {
		idToken = idTokens[0]
	}
	expires := time.Now().UTC().Add(ttl)
	if err := a.store.CreateSession(ctx, model.Session{
		IDHash:    hashToken(token),
		CSRFToken: csrf,
		UserID:    userID,
		IDToken:   idToken,
		ExpiresAt: expires,
	}); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.config.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	})
	return nil
}

func (a *Authenticator) logout(w http.ResponseWriter, r *http.Request) {
	_, session := currentUser(r.Context())
	cookie, err := r.Cookie(sessionCookie)
	if err == nil && cookie.Value != "" {
		if err := a.store.DeleteSession(r.Context(), hashToken(cookie.Value)); err != nil {
			writeError(w, http.StatusInternalServerError, "logout_failed", "Couldn't delete the session")
			return
		}
	}
	clearCookie(w, sessionCookie, a.config.SecureCookies)
	if a.endSessionEndpoint == "" {
		writeJSON(w, http.StatusOK, map[string]string{})
		return
	}
	logoutURL, err := url.Parse(a.endSessionEndpoint)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "logout_failed", "OIDC logout URL is invalid")
		return
	}
	query := logoutURL.Query()
	query.Set("client_id", a.config.ClientID)
	if session != nil && session.IDToken != "" {
		query.Set("id_token_hint", session.IDToken)
		if a.config.PostLogoutRedirectURL != "" {
			query.Set("post_logout_redirect_uri", strings.TrimSuffix(a.config.PostLogoutRedirectURL, "/")+"/")
		}
	}
	logoutURL.RawQuery = query.Encode()
	writeJSON(w, http.StatusOK, map[string]string{"logoutUrl": logoutURL.String()})
}

func requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, session := currentUser(r.Context())
		if session == nil {
			writeError(w, http.StatusUnauthorized, "not_authenticated", "Authentication is required")
			return
		}
		if session.CSRFToken == "" || r.Header.Get("X-CSRF-Token") != session.CSRFToken {
			writeError(w, http.StatusForbidden, "csrf_failed", "CSRF token is invalid")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func randomToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func hashToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

func clearCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0),
	})
}
