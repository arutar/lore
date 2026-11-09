package httpapi

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/policy"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

const tokenExchangeGrant = "urn:ietf:params:oauth:grant-type:token-exchange"
const accessTokenType = "urn:ietf:params:oauth:token-type:access_token"

type BearerAuthenticator interface {
	AuthenticateBearer(ctx context.Context, token string) (model.User, error)
}

type TokenExchange struct {
	store    store.Store
	auth     BearerAuthenticator
	issuer   string
	clientID string
	key      jose.JSONWebKey
	signer   jose.Signer
}

func NewTokenExchange(storage store.Store, auth BearerAuthenticator, issuer, clientID string, keyPaths ...string) (*TokenExchange, error) {
	keyPath := ""
	if len(keyPaths) > 0 {
		keyPath = keyPaths[0]
	}
	privateKey, err := loadOrCreateSigningKey(keyPath)
	if err != nil {
		return nil, err
	}
	thumbprint, err := (&jose.JSONWebKey{Key: privateKey.Public()}).Thumbprint(crypto.SHA256)
	if err != nil {
		return nil, err
	}
	key := jose.JSONWebKey{Key: privateKey, KeyID: hex.EncodeToString(thumbprint), Algorithm: string(jose.RS256), Use: "sig"}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, new(jose.SignerOptions).WithType("JWT"))
	if err != nil {
		return nil, err
	}
	return &TokenExchange{store: storage, auth: auth, issuer: strings.TrimSuffix(issuer, "/"), clientID: clientID, key: key, signer: signer}, nil
}

func loadOrCreateSigningKey(path string) (*rsa.PrivateKey, error) {
	if path != "" {
		encoded, err := os.ReadFile(path)
		if err == nil {
			block, _ := pem.Decode(encoded)
			if block == nil {
				return nil, errors.New("signing key PEM is invalid")
			}
			key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, err
			}
			privateKey, ok := key.(*rsa.PrivateKey)
			if !ok {
				return nil, errors.New("signing key must be RSA")
			}
			return privateKey, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil || path == "" {
		return privateKey, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".signing-key-*")
	if err != nil {
		return nil, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return nil, err
	}
	if _, err := temporary.Write(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})); err != nil {
		temporary.Close()
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return nil, err
	}
	return privateKey, nil
}

func (t *TokenExchange) discovery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                   t.issuer,
		"token_endpoint":           t.issuer + "/token",
		"jwks_uri":                 t.issuer + "/jwks",
		"grant_types_supported":    []string{tokenExchangeGrant},
		"response_types_supported": []string{},
	})
}

func (t *TokenExchange) jwks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{t.key.Public()}})
}

func (t *TokenExchange) exchange(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "Invalid form body")
		return
	}
	if r.Form.Get("grant_type") != tokenExchangeGrant || r.Form.Get("subject_token_type") != accessTokenType || r.Form.Get("requested_token_type") != accessTokenType || r.Form.Get("client_id") != t.clientID {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "Unsupported token exchange")
		return
	}
	user, err := t.auth.AuthenticateBearer(r.Context(), r.Form.Get("subject_token"))
	if err != nil || user.Disabled {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_grant", "Subject token is invalid")
		return
	}
	resources := r.Form["resource"]
	if len(resources) != 1 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "Exactly one repository resource is required")
		return
	}
	rawID, ok := strings.CutPrefix(resources[0], "lore-")
	id, validID := canonicalRepositoryID(rawID)
	if !ok || !validID {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "Repository resource is invalid")
		return
	}
	compactID := strings.ReplaceAll(id, "-", "")
	repository, err := t.store.GetRepository(r.Context(), id)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "Repository isn't available")
		return
	}
	grants, err := t.store.ListGrants(r.Context(), repository.ID)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "Permission lookup failed")
		return
	}
	if !policy.CanRead(&user, grants) {
		writeOAuthError(w, http.StatusForbidden, "access_denied", "Repository access isn't granted")
		return
	}
	permissions := []string{}
	if policy.CanWrite(&user, grants) {
		permissions = append(permissions, "write")
	}
	if policy.CanManage(&user, grants) {
		permissions = append(permissions, "owner")
	}
	now := time.Now().UTC()
	claims := map[string]any{
		"iss":                t.issuer,
		"sub":                user.ID,
		"aud":                []string{"lore-server"},
		"iat":                now.Unix(),
		"exp":                now.Add(5 * time.Minute).Unix(),
		"preferred_username": user.Username,
		"client_id":          t.clientID,
		"lore_access":        []map[string]any{{"resource_id": "lore-" + compactID, "permission": permissions}},
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "Couldn't encode token")
		return
	}
	signature, err := t.signer.Sign(payload)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "Couldn't sign token")
		return
	}
	token, err := signature.CompactSerialize()
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "Couldn't serialize token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":      token,
		"issued_token_type": accessTokenType,
		"token_type":        "Bearer",
		"expires_in":        300,
	})
}

func canonicalRepositoryID(value string) (string, bool) {
	if repositoryID.MatchString(value) {
		return strings.ToLower(value), true
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 {
		return "", false
	}
	encoded := hex.EncodeToString(decoded)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], true
}

func (t *TokenExchange) authenticate(ctx context.Context, token string) (model.User, error) {
	signature, err := jose.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return model.User{}, errors.New("invalid signed token")
	}
	payload, err := signature.Verify(t.key.Public())
	if err != nil {
		return model.User{}, err
	}
	var claims struct {
		Issuer   string   `json:"iss"`
		Subject  string   `json:"sub"`
		Audience []string `json:"aud"`
		Expires  int64    `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return model.User{}, err
	}
	if claims.Issuer != t.issuer || claims.Expires <= time.Now().Unix() || !slices.Contains(claims.Audience, "lore-server") {
		return model.User{}, errors.New("token claims are invalid")
	}
	user, err := t.store.GetUser(ctx, claims.Subject)
	if err != nil || user.Disabled {
		return model.User{}, errors.New("user is unavailable")
	}
	return user, nil
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}
