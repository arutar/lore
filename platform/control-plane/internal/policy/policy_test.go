package policy

import (
	"errors"
	"testing"

	"github.com/arutar/lore/platform/control-plane/internal/model"
)

func TestEffectiveAccess(t *testing.T) {
	user := &model.User{ID: "u1", Role: model.RoleUser, Groups: []string{"g1"}}
	grants := []model.Grant{
		{SubjectType: model.SubjectUser, Subject: "u1", Permission: model.PermissionRead},
		{SubjectType: model.SubjectGroup, Subject: "g1", Permission: model.PermissionWrite},
		{SubjectType: model.SubjectUser, Subject: "other", Permission: model.PermissionOwner},
	}

	if got := Effective(user, grants); got != model.PermissionWrite {
		t.Fatalf("effective permission = %q, want write", got)
	}
	if got := Effective(nil, grants); got != model.PermissionNone {
		t.Fatalf("anonymous permission = %q, want none", got)
	}
}

func TestPublicReadNeverGrantsWrite(t *testing.T) {
	grants := []model.Grant{{SubjectType: model.SubjectAnonymous, Permission: model.PermissionRead}}
	if !CanRead(nil, grants) {
		t.Fatal("anonymous caller cannot read public repository")
	}
	if CanWrite(nil, grants) || CanManage(nil, grants) {
		t.Fatal("anonymous public read grants mutation access")
	}
}

func TestAdministratorOverridesAndDisabledUserDenies(t *testing.T) {
	admin := &model.User{ID: "admin", Role: model.RoleAdmin}
	if got := Effective(admin, nil); got != model.PermissionOwner {
		t.Fatalf("administrator permission = %q, want owner", got)
	}
	admin.Disabled = true
	if got := Effective(admin, nil); got != model.PermissionNone {
		t.Fatalf("disabled administrator permission = %q, want none", got)
	}
}

func TestAdditiveHighestGrantWins(t *testing.T) {
	user := &model.User{ID: "u1", Role: model.RoleUser}
	grants := []model.Grant{
		{SubjectType: model.SubjectAuthenticated, Permission: model.PermissionRead},
		{SubjectType: model.SubjectUser, Subject: "u1", Permission: model.PermissionOwner},
	}
	if got := Effective(user, grants); got != model.PermissionOwner {
		t.Fatalf("effective permission = %q, want owner", got)
	}
}

func TestValidateGrant(t *testing.T) {
	tests := []struct {
		name  string
		grant model.Grant
		valid bool
	}{
		{name: "anonymous read", grant: model.Grant{SubjectType: model.SubjectAnonymous, Permission: model.PermissionRead}, valid: true},
		{name: "authenticated read", grant: model.Grant{SubjectType: model.SubjectAuthenticated, Permission: model.PermissionRead}, valid: true},
		{name: "anonymous write", grant: model.Grant{SubjectType: model.SubjectAnonymous, Permission: model.PermissionWrite}},
		{name: "anonymous subject", grant: model.Grant{SubjectType: model.SubjectAnonymous, Subject: "someone", Permission: model.PermissionRead}},
		{name: "user without id", grant: model.Grant{SubjectType: model.SubjectUser, Permission: model.PermissionRead}},
		{name: "group owner", grant: model.Grant{SubjectType: model.SubjectGroup, Subject: "g1", Permission: model.PermissionOwner}, valid: true},
		{name: "unknown permission", grant: model.Grant{SubjectType: model.SubjectUser, Subject: "u1", Permission: "everything"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateGrant(test.grant)
			if test.valid && err != nil {
				t.Fatalf("valid grant rejected: %v", err)
			}
			if !test.valid && !errors.Is(err, ErrInvalidGrant) {
				t.Fatalf("invalid grant error = %v, want ErrInvalidGrant", err)
			}
		})
	}
}
