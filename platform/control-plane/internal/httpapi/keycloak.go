package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"golang.org/x/oauth2/clientcredentials"

	"github.com/arutar/lore/platform/control-plane/internal/directory"
	"github.com/arutar/lore/platform/control-plane/internal/model"
)

var ErrAccountExists = errors.New("account already exists")
var ErrAccountNotFound = errors.New("account not found")
var ErrNotFederated = errors.New("account is not federated with Active Directory")

type RegistrationRealm interface {
	SetRegistrationAllowed(ctx context.Context, enabled bool) error
}

type AccountManager interface {
	CreateLocalUser(ctx context.Context, email, displayName, password string) (model.User, error)
	SetLocalUserEnabled(ctx context.Context, id string, enabled bool) error
	// GetFederatedUser finds the identity provider account of a directory user by exact
	// username or email; DirectoryID carries its LDAP_ID (objectGUID).
	GetFederatedUser(ctx context.Context, username string) (model.User, error)
	DirectoryLinks
	SyncFederatedUsers(ctx context.Context) error
	LogoutUser(ctx context.Context, id string) error
}

type KeycloakRealmAdmin struct {
	realmURL string
	client   *http.Client
}

func NewKeycloakRealmAdmin(issuer, tokenURL, clientID, secret string) (*KeycloakRealmAdmin, error) {
	realmIndex := strings.Index(issuer, "/realms/")
	if realmIndex < 1 || tokenURL == "" || clientID == "" || secret == "" {
		return nil, fmt.Errorf("Keycloak admin URL and service credentials are required")
	}
	oauth := clientcredentials.Config{ClientID: clientID, ClientSecret: secret, TokenURL: tokenURL}
	return &KeycloakRealmAdmin{
		realmURL: issuer[:realmIndex] + "/admin" + issuer[realmIndex:],
		client:   oauth.Client(context.Background()),
	}, nil
}

func (a *KeycloakRealmAdmin) SetRegistrationAllowed(ctx context.Context, enabled bool) error {
	body, err := json.Marshal(map[string]bool{"registrationAllowed": enabled})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, a.realmURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Keycloak realm update returned %s", response.Status)
	}
	return nil
}

func (a *KeycloakRealmAdmin) CreateLocalUser(ctx context.Context, email, displayName, password string) (model.User, error) {
	body, err := json.Marshal(map[string]any{
		"username": email, "email": email, "emailVerified": true, "enabled": true,
		"firstName": displayName, "lastName": "-",
		"credentials": []map[string]any{{"type": "password", "value": password, "temporary": true}},
	})
	if err != nil {
		return model.User{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.realmURL+"/users", bytes.NewReader(body))
	if err != nil {
		return model.User{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return model.User{}, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode == http.StatusConflict {
		return model.User{}, ErrAccountExists
	}
	if response.StatusCode != http.StatusCreated {
		return model.User{}, fmt.Errorf("Keycloak user creation returned %s", response.Status)
	}
	id := path.Base(response.Header.Get("Location"))
	if id == "." || id == "/" || id == "" {
		return model.User{}, errors.New("Keycloak user creation returned no account ID")
	}
	return model.User{ID: id, Username: email, Email: email, DisplayName: displayName, Role: model.RoleUser, Groups: []string{}, Source: "local"}, nil
}

func (a *KeycloakRealmAdmin) SetLocalUserEnabled(ctx context.Context, id string, enabled bool) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.realmURL+"/users/"+id, nil)
	if err != nil {
		return err
	}
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Keycloak user lookup returned %s", response.Status)
	}
	var account struct {
		FederationLink string `json:"federationLink"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&account); err != nil {
		return err
	}
	if account.FederationLink != "" {
		return nil
	}
	body, err := json.Marshal(map[string]bool{"enabled": enabled})
	if err != nil {
		return err
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodPut, a.realmURL+"/users/"+id, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err = a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Keycloak user update returned %s", response.Status)
	}
	return nil
}

type keycloakAccount struct {
	ID             string              `json:"id"`
	Username       string              `json:"username"`
	Email          string              `json:"email"`
	FirstName      string              `json:"firstName"`
	LastName       string              `json:"lastName"`
	FederationLink string              `json:"federationLink"`
	Enabled        bool                `json:"enabled"`
	Attributes     map[string][]string `json:"attributes"`
}

// directoryID is the objectGUID Keycloak's LDAP provider stores for a federated account.
func (k keycloakAccount) directoryID() string {
	if k.FederationLink == "" {
		return ""
	}
	if values := k.Attributes["LDAP_ID"]; len(values) > 0 {
		return directory.NormalizeGUID(values[0])
	}
	return ""
}

func (a *KeycloakRealmAdmin) GetFederatedUser(ctx context.Context, username string) (model.User, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 256 {
		return model.User{}, ErrAccountNotFound
	}
	// Exact lookups: a substring search with a result limit can miss the account, and an
	// exact username lookup also makes Keycloak import a directory account on first use.
	var account keycloakAccount
	found := false
	for _, field := range []string{"username", "email"} {
		var accounts []keycloakAccount
		endpoint := a.realmURL + "/users?" + field + "=" + url.QueryEscape(username) + "&exact=true&briefRepresentation=false&max=2"
		if err := a.getJSON(ctx, endpoint, &accounts); err != nil {
			return model.User{}, err
		}
		if len(accounts) > 1 {
			return model.User{}, fmt.Errorf("more than one identity provider account matches %s %q", field, username)
		}
		if len(accounts) == 1 && accounts[0].ID != "" {
			account, found = accounts[0], true
			break
		}
	}
	if !found {
		return model.User{}, ErrAccountNotFound
	}
	if account.FederationLink == "" {
		return model.User{}, ErrNotFederated
	}
	displayName := strings.TrimSpace(account.FirstName + " " + account.LastName)
	if displayName == "" {
		displayName = account.Username
	}
	return model.User{
		ID: account.ID, Username: account.Username, Email: account.Email, DisplayName: displayName,
		Role: model.RoleUser, Source: SourceDirectory, Disabled: !account.Enabled, Groups: []string{},
		DirectoryID: account.directoryID(),
	}, nil
}

// DirectoryLink returns the objectGUID an account is federated with, or "" for a local one.
func (a *KeycloakRealmAdmin) DirectoryLink(ctx context.Context, userID string) (string, error) {
	var account keycloakAccount
	err := a.getJSON(ctx, a.realmURL+"/users/"+url.PathEscape(userID), &account)
	if errors.Is(err, ErrAccountNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if account.FederationLink != "" && account.directoryID() == "" {
		return "", fmt.Errorf("federated account %s has no LDAP_ID", userID)
	}
	return account.directoryID(), nil
}

func (a *KeycloakRealmAdmin) getJSON(ctx context.Context, endpoint string, destination any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return ErrAccountNotFound
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Keycloak request returned %s", response.Status)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(destination)
}

func (a *KeycloakRealmAdmin) SyncFederatedUsers(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.realmURL+"/components?type=org.keycloak.storage.UserStorageProvider", nil)
	if err != nil {
		return err
	}
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Keycloak federation lookup returned %s", response.Status)
	}
	var components []struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerId"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&components); err != nil {
		return err
	}
	providerID := ""
	for _, component := range components {
		if component.ProviderID == "ldap" && component.ID != "" {
			providerID = component.ID
			break
		}
	}
	if providerID == "" {
		return errors.New("Keycloak LDAP provider wasn't found")
	}
	endpoint := a.realmURL + "/user-storage/" + url.PathEscape(providerID) + "/sync?action=triggerFullSync"
	request, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	response, err = a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Keycloak federation synchronization returned %s", response.Status)
	}
	return nil
}

func (a *KeycloakRealmAdmin) LogoutUser(ctx context.Context, id string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.realmURL+"/users/"+id+"/logout", nil)
	if err != nil {
		return err
	}
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Keycloak user logout returned %s", response.Status)
	}
	return nil
}
