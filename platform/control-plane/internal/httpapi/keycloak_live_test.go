package httpapi

import (
	"os"
	"strings"
	"testing"
)

func TestKeycloakLiveFederatedUsers(t *testing.T) {
	if os.Getenv("LORE_KEYCLOAK_LIVE_TEST") != "1" {
		t.Skip("live Keycloak test is disabled")
	}
	admin, err := NewKeycloakRealmAdmin(
		os.Getenv("OIDC_ISSUER"),
		os.Getenv("LORE_SERVICE_TOKEN_URL"),
		os.Getenv("LORE_SERVICE_CLIENT_ID"),
		os.Getenv("LORE_SERVICE_CLIENT_SECRET"),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, lookup := range strings.Split(os.Getenv("LORE_TEST_FEDERATED_LOOKUPS"), ",") {
		user, err := admin.GetFederatedUser(t.Context(), strings.TrimSpace(lookup))
		if err != nil {
			t.Errorf("lookup %q: %v", lookup, err)
			continue
		}
		if user.ID == "" || user.Source != "ad" {
			t.Errorf("lookup %q returned %+v", lookup, user)
		}
	}
}
