package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/oauth2/clientcredentials"
)

type RegistrationRealm interface {
	SetRegistrationAllowed(ctx context.Context, enabled bool) error
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
