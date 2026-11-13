package directory

import (
	"context"
	"os"
	"testing"
)

func TestLDAPLiveAccount(t *testing.T) {
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
	user, err := directory.FindUser(context.Background(), os.Getenv("LORE_LDAP_TEST_USER"))
	if err != nil {
		t.Fatal(err)
	}
	if user.ID == "" {
		t.Fatal("the account has no objectGUID")
	}
	account, err := directory.LookupAccount(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if account.Username != user.Username || len(account.Groups) == 0 {
		t.Fatalf("lookup by objectGUID returned %q with %d groups", account.Username, len(account.Groups))
	}
	t.Logf("%s: disabled=%v, %d stable group IDs", account.Username, account.Disabled, len(account.Groups))
}
