package directory

import (
	"context"
	"os"
	"testing"
)

func TestLDAPLiveGroupIDs(t *testing.T) {
	if os.Getenv("LORE_LDAP_LIVE_TEST") != "1" {
		t.Skip("live LDAP test is disabled")
	}
	directory, err := NewLDAPFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if directory == nil {
		t.Fatal("LORE_LDAP_URL is required")
	}
	groups, err := directory.GroupIDs(context.Background(), os.Getenv("LORE_LDAP_TEST_USER"))
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) == 0 {
		t.Fatal("expected at least one group")
	}
	t.Logf("received %d stable group IDs", len(groups))
}
