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
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/arutar/lore/platform/control-plane/internal/directory"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

const (
	sessionCookie  = "lore_session"
	stateCookie    = "lore_oidc_state"
	nonceCookie    = "lore_oidc_nonce"
	verifierCookie = "lore_oidc_verifier"
)

type GroupDirectory interface {
	GroupIDs(ctx context.Context, username string) ([]string, error)
}

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
	GroupDirectory        GroupDirectory
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
	auth.verifier = provider.Verifier(&oidc.Config{ClientID: config.ClientID})
	auth.serviceVerifier = provider.Verifier(&oidc.Config{SkipClientIDCheck: true})
	auth.logoutVerifier = provider.Verifier(&oidc.Config{SkipClientIDCheck: true, SkipExpiryCheck: true})
	endpoint := provider.Endpoint()
	if config.InternalIssuer != "" {
		keys := oidc.NewRemoteKeySet(ctx, config.InternalIssuer+"/protocol/openid-connect/certs")
		auth.verifier = oidc.NewVerifier(config.Issuer, keys, &oidc.Config{ClientID: config.ClientID})
		auth.serviceVerifier = oidc.NewVerifier(config.Issuer, keys, &oidc.Config{SkipClientIDCheck: true})
		auth.logoutVerifier = oidc.NewVerifier(config.Issuer, keys, &oidc.Config{SkipClientIDCheck: true, SkipExpiryCheck: true})
		endpoint.TokenURL = config.InternalIssuer + "/protocol/openid-connect/token"
	}
	auth.oauth = oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		RedirectURL:  config.RedirectURL,
		Endpoint:     endpoint,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email", "groups"},
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
		if err != nil || user.Disabled {
			clearCookie(w, sessionCookie, a.config.SecureCookies)
			next.ServeHTTP(w, r)
			return
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
	var claims struct {
		Username string   `json:"preferred_username"`
		Email    string   `json:"email"`
		Name     string   `json:"name"`
		Groups   []string `json:"groups"`
		Source   string   `json:"idp"`
	}
	if err := idToken.Claims(&claims); err != nil {
		writeError(w, http.StatusUnauthorized, "claims_invalid", "OIDC claims are invalid")
		return
	}
	if claims.Username == "" {
		claims.Username = claims.Email
	}
	if claims.Username == "" {
		claims.Username = idToken.Subject
	}
	if claims.Name == "" {
		claims.Name = claims.Username
	}
	if claims.Source == "" {
		claims.Source = "oidc"
	}
	groups, err := a.groupIDs(r.Context(), claims.Username, claims.Groups)
	if err != nil {
		writeError(w, http.StatusBadGateway, "directory_sync_failed", "Couldn't read directory groups")
		return
	}
	user, err := a.store.UpsertUser(r.Context(), model.User{
		ID:          idToken.Subject,
		Username:    claims.Username,
		Email:       claims.Email,
		DisplayName: claims.Name,
		Role:        model.RoleUser,
		Groups:      groups,
		Source:      claims.Source,
	})
	if err != nil {
		log.Printf("OIDC user sync failed: %v", err)
		writeError(w, http.StatusInternalServerError, "user_sync_failed", "Couldn't synchronize the account")
		return
	}
	if a.config.BootstrapAdmin != "" && (user.ID == a.config.BootstrapAdmin || user.Username == a.config.BootstrapAdmin) && user.Role != model.RoleAdmin {
		user, err = a.store.UpdateUser(r.Context(), user.ID, user.ID, model.RoleAdmin, user.Disabled)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "bootstrap_failed", "Couldn't initialize the administrator")
			return
		}
	}
	if user.Disabled {
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

func (a *Authenticator) AuthenticateBearer(ctx context.Context, token string) (model.User, error) {
	if a.serviceVerifier != nil {
		if user, err := a.authenticateOIDCBearer(ctx, token); err == nil {
			return user, nil
		}
	}
	if a.tokenExchange != nil {
		return a.tokenExchange.authenticate(ctx, token)
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
	return idToken.Subject, nil
}

func (a *Authenticator) groupIDs(ctx context.Context, username string, fallback []string) ([]string, error) {
	if a.config.GroupDirectory == nil || strings.Contains(username, "@") {
		return fallback, nil
	}
	groups, err := a.config.GroupDirectory.GroupIDs(ctx, username)
	if errors.Is(err, directory.ErrUserNotFound) {
		return fallback, nil
	}
	return groups, err
}

func (a *Authenticator) SetTokenExchange(exchange *TokenExchange) {
	a.tokenExchange = exchange
}

func (a *Authenticator) authenticateOIDCBearer(ctx context.Context, token string) (model.User, error) {
	idToken, err := a.serviceVerifier.Verify(ctx, token)
	if err != nil {
		return model.User{}, err
	}
	user, err := a.store.GetUser(ctx, idToken.Subject)
	if err == nil {
		if user.Disabled {
			return model.User{}, errors.New("account is disabled")
		}
		return user, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return model.User{}, err
	}
	var claims struct {
		Username string   `json:"preferred_username"`
		Email    string   `json:"email"`
		Name     string   `json:"name"`
		Groups   []string `json:"groups"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return model.User{}, err
	}
	if claims.Username == "" {
		claims.Username = claims.Email
	}
	if claims.Username == "" {
		claims.Username = idToken.Subject
	}
	if claims.Name == "" {
		claims.Name = claims.Username
	}
	groups, err := a.groupIDs(ctx, claims.Username, claims.Groups)
	if err != nil {
		return model.User{}, err
	}
	user, err = a.store.UpsertUser(ctx, model.User{
		ID: idToken.Subject, Username: claims.Username, Email: claims.Email,
		DisplayName: claims.Name, Role: model.RoleUser, Groups: groups, Source: "oidc",
	})
	if err != nil {
		return model.User{}, err
	}
	if a.config.BootstrapAdmin != "" && (user.ID == a.config.BootstrapAdmin || user.Username == a.config.BootstrapAdmin) && user.Role != model.RoleAdmin {
		return a.store.UpdateUser(ctx, user.ID, user.ID, model.RoleAdmin, false)
	}
	return user, nil
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
