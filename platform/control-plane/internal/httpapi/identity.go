package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/arutar/lore/platform/control-plane/internal/directory"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

// Account sources stored in model.User.Source.
const (
	SourceDirectory = "ad"
	SourceLocal     = "local"
	SourceService   = "service"
)

const (
	defaultDirectoryRefresh     = 5 * time.Minute
	defaultDirectoryOutageGrace = 8 * time.Hour
	defaultAudience             = "lore-server"
	defaultNativeClientID       = "lore-native"
)

// ErrDirectoryUnavailable means the directory couldn't be read and the account's last
// known groups are too old to rely on.
var ErrDirectoryUnavailable = errors.New("directory is unavailable")

// ErrAccountInactive means the account is disabled here or in the directory.
var ErrAccountInactive = errors.New("account is disabled")

// errNotLoginToken means the token isn't an identity provider token for Lore at all, so
// another kind of token may still be tried. Any later failure is final.
var errNotLoginToken = errors.New("not a login token")

// DirectoryAccounts reads Active Directory accounts by objectGUID.
type DirectoryAccounts interface {
	LookupAccount(ctx context.Context, id string) (directory.Account, error)
}

// DirectoryLinks resolves which directory account, if any, an identity provider account
// is federated with. It's the fallback for tokens that don't carry the ldap_id claim.
type DirectoryLinks interface {
	DirectoryLink(ctx context.Context, userID string) (string, error)
}

// identityClaims are the claims both the ID token and the native access token carry.
type identityClaims struct {
	Subject         string   `json:"sub"`
	Username        string   `json:"preferred_username"`
	Email           string   `json:"email"`
	Name            string   `json:"name"`
	Groups          []string `json:"groups"`
	DirectoryID     string   `json:"ldap_id"`
	AuthorizedParty string   `json:"azp"`
}

func (c *identityClaims) normalize() {
	if c.Username == "" {
		c.Username = c.Email
	}
	if c.Username == "" {
		c.Username = c.Subject
	}
	// Local accounts are created in Keycloak with the last name "-" (Keycloak requires one,
	// see CreateLocalUser), which Keycloak appends to the name claim.
	c.Name = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(c.Name), " -"))
	if c.Name == "" {
		c.Name = c.Username
	}
}

// signIn turns a verified identity into the stored account: it decides whether the
// account comes from Active Directory by its federation link, never by the shape of its
// name, reads groups for directory accounts by objectGUID, and applies the one-time
// bootstrap administrator. The caller still has to check Active().
func (a *Authenticator) signIn(ctx context.Context, claims identityClaims) (model.User, error) {
	claims.normalize()
	existing, err := a.store.GetUser(ctx, claims.Subject)
	known := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return model.User{}, err
	}
	if claims.AuthorizedParty != "" && claims.AuthorizedParty == a.config.ServiceClientID {
		return a.signInService(ctx, claims, existing, known)
	}

	directoryID := directory.NormalizeGUID(claims.DirectoryID)
	if directoryID == "" && known && existing.Federated() {
		directoryID = existing.DirectoryID
	}
	if directoryID == "" && a.config.DirectoryLinks != nil {
		link, err := a.config.DirectoryLinks.DirectoryLink(ctx, claims.Subject)
		if err != nil {
			return model.User{}, fmt.Errorf("resolve directory link: %w", err)
		}
		directoryID = directory.NormalizeGUID(link)
	}

	user := model.User{
		ID: claims.Subject, Username: claims.Username, Email: claims.Email, DisplayName: claims.Name,
		Role: model.RoleUser, Source: SourceLocal, DirectoryID: directoryID,
	}
	if directoryID != "" && a.config.Directory != nil {
		var previous *model.User
		if known && existing.DirectoryID == directoryID {
			previous = &existing
		}
		state, err := a.directoryState(ctx, previous, directoryID)
		if err != nil {
			return model.User{}, err
		}
		user.Source = SourceDirectory
		user.Groups = state.Groups
		user.DirectoryDisabled = state.Disabled
		user.GroupsSyncedAt = &state.SyncedAt
	} else {
		// A local account, or a directory account while this control plane has no LDAP
		// connection: the identity provider's groups are all there is.
		if directoryID != "" {
			user.Source = SourceDirectory
		}
		now := time.Now().UTC()
		user.Groups = claims.Groups
		user.GroupsSyncedAt = &now
	}
	if user.AccessDenied, user.Role, err = a.directoryAccess(ctx, user); err != nil {
		return model.User{}, err
	}
	inherits, err := a.takesOverAdministrator(ctx, user, existing, known)
	if err != nil {
		return model.User{}, err
	}
	stored, err := a.store.UpsertUser(ctx, user)
	if err != nil {
		return model.User{}, err
	}
	// The upsert keeps a known account's role; a directory account's comes from the list.
	if stored, err = a.applyAccess(ctx, stored); err != nil {
		return model.User{}, err
	}
	return a.bootstrap(ctx, stored, inherits)
}

// takesOverAdministrator reports whether the local bootstrap account is about to take its
// name from another account that holds the administrator role, typically an Active
// Directory account also called "admin" that signed in before setup created the local one.
// Keycloak checks its own users before the directory, so from now on only the local account
// can sign in under that name; it inherits the role so the installation keeps a working
// administrator. The former record keeps its role but can no longer sign in as "admin".
func (a *Authenticator) takesOverAdministrator(ctx context.Context, user, existing model.User, known bool) (bool, error) {
	name := a.config.BootstrapAdmin
	if name == "" || user.Source != SourceLocal || user.Username != name || (known && existing.Role == model.RoleAdmin) {
		return false, nil
	}
	page, err := a.store.ListUsers(ctx, name, "", 100)
	if err != nil {
		return false, err
	}
	for _, other := range page.Items {
		if other.ID != user.ID && other.Username == name && other.Role == model.RoleAdmin {
			return true, nil
		}
	}
	return false, nil
}

func (a *Authenticator) signInService(ctx context.Context, claims identityClaims, existing model.User, known bool) (model.User, error) {
	if known && existing.Source == SourceService && existing.Username == claims.Username {
		return existing, nil
	}
	now := time.Now().UTC()
	return a.store.UpsertUser(ctx, model.User{
		ID: claims.Subject, Username: claims.Username, DisplayName: "Lore service (automation)",
		Role: model.RoleUser, Source: SourceService, Groups: []string{}, GroupsSyncedAt: &now,
	})
}

// bootstrap makes the configured account the first administrator, once: only while no
// active administrator exists, and never a directory or service account, so an AD
// account that happens to be called "admin" can't claim the role. inherits is set when
// the local account has just taken the name from an administrator (takesOverAdministrator).
func (a *Authenticator) bootstrap(ctx context.Context, user model.User, inherits bool) (model.User, error) {
	name := a.config.BootstrapAdmin
	if name == "" || user.Role == model.RoleAdmin || user.Source != SourceLocal || !user.Active() {
		return user, nil
	}
	if user.ID != name && user.Username != name {
		return user, nil
	}
	if inherits {
		log.Printf("Local account %s took its name over from an administrator and inherits the role", user.Username)
		return a.store.UpdateUser(ctx, "", user.ID, model.RoleAdmin, false)
	}
	exists, err := a.store.HasActiveAdmin(ctx)
	if err != nil || exists {
		return user, err
	}
	log.Printf("Bootstrap administrator assigned to %s", user.Username)
	return a.store.UpdateUser(ctx, "", user.ID, model.RoleAdmin, false)
}

// refresh re-reads a directory account's groups and status once they're older than the
// refresh interval, so removing someone from a group or disabling them in AD takes effect
// within minutes instead of at the next interactive sign-in.
func (a *Authenticator) refresh(ctx context.Context, user model.User) (model.User, error) {
	if !user.Federated() || a.config.Directory == nil {
		return user, nil
	}
	interval := a.config.DirectoryRefresh
	if interval <= 0 {
		interval = defaultDirectoryRefresh
	}
	if user.GroupsSyncedAt != nil && time.Since(*user.GroupsSyncedAt) < interval {
		return user, nil
	}
	// During an outage the stored state keeps its old time, so without this every request
	// would wait on the unreachable directory again; try at most once per interval.
	if last, ok := a.directoryAttempts.Load(user.ID); ok && time.Since(last.(time.Time)) < interval && user.GroupsSyncedAt != nil {
		if grace := a.outageGrace(); time.Since(*user.GroupsSyncedAt) < grace {
			return user, nil
		}
		return model.User{}, ErrDirectoryUnavailable
	}
	a.directoryAttempts.Store(user.ID, time.Now())
	state, err := a.directoryState(ctx, &user, user.DirectoryID)
	if err != nil {
		return model.User{}, err
	}
	if user.GroupsSyncedAt != nil && state.SyncedAt.Equal(*user.GroupsSyncedAt) {
		return user, nil // kept the last known state through a directory outage
	}
	updated, err := a.store.UpdateDirectoryState(ctx, user.ID, state)
	if err != nil {
		return model.User{}, err
	}
	// Groups may have changed, and with them whether the account is admitted.
	return a.applyAccess(ctx, updated)
}

// directoryAccess decides what the access list gives a directory account: whether it's kept
// out (neither it nor any of its groups, nested ones included, is admitted) and its role, the
// highest of its own entry and its groups' entries. Local and service accounts aren't on the
// list: they're always admitted and keep the role an administrator gave them.
func (a *Authenticator) directoryAccess(ctx context.Context, user model.User) (denied bool, role model.Role, err error) {
	if !user.Federated() || user.Source == SourceService {
		return false, user.Role, nil
	}
	entries, err := a.store.ListAccess(ctx)
	if err != nil {
		return false, user.Role, err
	}
	admitted, role := model.ResolveAccess(entries, user.DirectoryID, user.Groups)
	return !admitted, role, nil
}

// applyAccess brings a directory account's stored access and role in line with the list.
func (a *Authenticator) applyAccess(ctx context.Context, user model.User) (model.User, error) {
	if !user.Federated() || user.Source == SourceService {
		return user, nil
	}
	denied, role, err := a.directoryAccess(ctx, user)
	if err != nil || (denied == user.AccessDenied && role == user.Role) {
		return user, err
	}
	return a.store.ApplyDirectoryAccess(ctx, user.ID, denied, role)
}

// RecheckAccess applies the access list to every stored directory account, right after an
// administrator changes it: a removed entry closes its members' sessions at their next
// request instead of at the next directory refresh.
func (a *Authenticator) RecheckAccess(ctx context.Context) error {
	cursor := ""
	for {
		page, err := a.store.ListUsers(ctx, "", cursor, 500)
		if err != nil {
			return err
		}
		for _, user := range page.Items {
			if _, err := a.applyAccess(ctx, user); err != nil {
				return err
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}

// directoryState reads the account from the directory. During an outage it keeps the
// last known state for the grace period and then refuses, so an outage can delay a
// revocation by at most the grace period but never grants anything new.
func (a *Authenticator) directoryState(ctx context.Context, previous *model.User, directoryID string) (store.DirectoryState, error) {
	account, err := a.config.Directory.LookupAccount(ctx, directoryID)
	now := time.Now().UTC()
	switch {
	case err == nil:
		return store.DirectoryState{DirectoryID: directoryID, Groups: account.Groups, Disabled: account.Disabled, SyncedAt: now}, nil
	case errors.Is(err, directory.ErrUserNotFound):
		// Deleted or moved out of the searched tree: no access until it's back.
		return store.DirectoryState{DirectoryID: directoryID, Groups: []string{}, Disabled: true, SyncedAt: now}, nil
	}
	grace := a.outageGrace()
	if previous != nil && previous.GroupsSyncedAt != nil && now.Sub(*previous.GroupsSyncedAt) < grace {
		log.Printf("Directory lookup for %s failed, keeping groups from %s: %v", previous.Username, previous.GroupsSyncedAt.Format(time.RFC3339), err)
		return store.DirectoryState{
			DirectoryID: directoryID, Groups: previous.Groups, Disabled: previous.DirectoryDisabled, SyncedAt: *previous.GroupsSyncedAt,
		}, nil
	}
	log.Printf("Directory lookup for %s failed: %v", directoryID, err)
	return store.DirectoryState{}, ErrDirectoryUnavailable
}

func (a *Authenticator) outageGrace() time.Duration {
	if a.config.DirectoryOutageGrace <= 0 {
		return defaultDirectoryOutageGrace
	}
	return a.config.DirectoryOutageGrace
}

// authenticateLogin verifies an access token the identity provider issued to the native
// client (or to the control plane's own service account) and returns its current account.
func (a *Authenticator) authenticateLogin(ctx context.Context, token string) (model.User, error) {
	if a.serviceVerifier == nil {
		return model.User{}, errNotLoginToken
	}
	idToken, err := a.serviceVerifier.Verify(ctx, token)
	if err != nil {
		return model.User{}, fmt.Errorf("%w: %v", errNotLoginToken, err)
	}
	var claims identityClaims
	if err := idToken.Claims(&claims); err != nil {
		return model.User{}, fmt.Errorf("%w: %v", errNotLoginToken, err)
	}
	if !slices.Contains(a.bearerClients(), claims.AuthorizedParty) {
		return model.User{}, fmt.Errorf("%w: tokens of client %q aren't accepted", errNotLoginToken, claims.AuthorizedParty)
	}
	user, err := a.store.GetUser(ctx, claims.Subject)
	switch {
	case errors.Is(err, store.ErrNotFound) || (err == nil && user.GroupsSyncedAt == nil):
		// First use, or an account stored before directory links existed.
		user, err = a.signIn(ctx, claims)
	case err == nil && claims.AuthorizedParty == a.config.ServiceClientID:
		user, err = a.signIn(ctx, claims)
	case err == nil && user.Federated():
		user, err = a.refresh(ctx, user)
	case err == nil && !slices.Equal(user.Groups, claims.Groups) && claims.Groups != nil:
		now := time.Now().UTC()
		user, err = a.store.UpdateDirectoryState(ctx, user.ID, store.DirectoryState{Groups: claims.Groups, SyncedAt: now})
	}
	if err != nil {
		return model.User{}, err
	}
	if !user.Active() {
		return model.User{}, ErrAccountInactive
	}
	return user, nil
}

func (a *Authenticator) bearerClients() []string {
	native := a.config.NativeClientID
	if native == "" {
		native = defaultNativeClientID
	}
	clients := []string{native}
	if a.config.ServiceClientID != "" {
		clients = append(clients, a.config.ServiceClientID)
	}
	return clients
}
