package directory

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

const nestedGroupsMatchingRule = "1.2.840.113556.1.4.1941"

var ErrUserNotFound = errors.New("LDAP user not found")

type Group struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	DistinguishedName string `json:"distinguishedName"`
	Description       string `json:"description,omitempty"`
}

type User struct {
	// ID is the account's objectGUID, the same value Keycloak stores as LDAP_ID.
	ID                string `json:"id,omitempty"`
	Username          string `json:"username"`
	Email             string `json:"email"`
	DisplayName       string `json:"displayName"`
	DistinguishedName string `json:"-"`
}

type LDAPConfig struct {
	URLs               []string
	BindDN             string
	BindPassword       string
	BaseDN             string
	UsernameAttribute  string
	InsecureSkipVerify bool
	StartTLS           bool
	Timeout            time.Duration
}

type LDAPDirectory struct {
	config LDAPConfig
}

func NewLDAPFromEnv() (*LDAPDirectory, error) {
	rawURLs := os.Getenv("LORE_LDAP_URL")
	if rawURLs == "" {
		return nil, nil
	}
	config := LDAPConfig{
		URLs:              strings.FieldsFunc(rawURLs, func(r rune) bool { return r == ',' || r == ' ' }),
		BindDN:            os.Getenv("LORE_LDAP_BIND_DN"),
		BindPassword:      os.Getenv("LORE_LDAP_BIND_PASSWORD"),
		BaseDN:            os.Getenv("LORE_LDAP_BASE_DN"),
		UsernameAttribute: os.Getenv("LORE_LDAP_USERNAME_ATTRIBUTE"),
		Timeout:           60 * time.Second,
	}
	if config.UsernameAttribute == "" {
		config.UsernameAttribute = "sAMAccountName"
	}
	if config.BindDN == "" || config.BindPassword == "" || config.BaseDN == "" {
		return nil, fmt.Errorf("LORE_LDAP_BIND_DN, LORE_LDAP_BIND_PASSWORD and LORE_LDAP_BASE_DN are required with LORE_LDAP_URL")
	}
	var err error
	config.InsecureSkipVerify, err = boolEnv("LORE_LDAP_INSECURE_SKIP_VERIFY", false)
	if err != nil {
		return nil, err
	}
	config.StartTLS, err = boolEnv("LORE_LDAP_STARTTLS", false)
	if err != nil {
		return nil, err
	}
	if value := os.Getenv("LORE_LDAP_TIMEOUT"); value != "" {
		config.Timeout, err = time.ParseDuration(value)
		if err != nil || config.Timeout < time.Second || config.Timeout > time.Minute {
			return nil, fmt.Errorf("LORE_LDAP_TIMEOUT must be between 1s and 1m")
		}
	}
	return &LDAPDirectory{config: config}, nil
}

// Account is the directory state that decides a user's access: whether the account
// still exists and is enabled, and which groups it belongs to, nested ones included.
type Account struct {
	User
	Disabled bool
	Groups   []string
}

// accountDisabled is the ACCOUNTDISABLE bit of userAccountControl.
const accountDisabled = 0x2

// LookupAccount reads an account by objectGUID. Looking the account up by its stable ID
// rather than by name means a renamed account keeps its access and a local account that
// happens to share a name with a directory account never inherits its groups.
func (d *LDAPDirectory) LookupAccount(ctx context.Context, id string) (Account, error) {
	filter, err := objectGUIDFilter(id)
	if err != nil {
		return Account{}, err
	}
	var failures []error
	for _, address := range d.config.URLs {
		account, err := d.lookupAccountOnServer(ctx, address, filter)
		if err == nil || errors.Is(err, ErrUserNotFound) {
			// A server that answers "no such account" is authoritative; don't let a
			// second, unreachable server turn that answer into an outage.
			return account, err
		}
		failures = append(failures, fmt.Errorf("%s: %w", address, err))
	}
	return Account{}, errors.Join(failures...)
}

func (d *LDAPDirectory) lookupAccountOnServer(ctx context.Context, address, filter string) (Account, error) {
	connection, err := d.connect(ctx, address)
	if err != nil {
		return Account{}, err
	}
	defer connection.Close()
	result, err := connection.Search(ldap.NewSearchRequest(
		d.config.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, int(d.config.Timeout.Seconds()), false,
		filter,
		[]string{d.config.UsernameAttribute, "mail", "displayName", "cn", "objectGUID", "userAccountControl"},
		nil,
	))
	if err != nil {
		return Account{}, err
	}
	if len(result.Entries) == 0 {
		return Account{}, ErrUserNotFound
	}
	if len(result.Entries) != 1 {
		return Account{}, fmt.Errorf("expected one LDAP account, found %d", len(result.Entries))
	}
	entry := result.Entries[0]
	account := Account{User: userFromEntry(entry, d.config.UsernameAttribute)}
	if value := entry.GetAttributeValue("userAccountControl"); value != "" {
		flags, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return Account{}, fmt.Errorf("userAccountControl %q is invalid", value)
		}
		account.Disabled = flags&accountDisabled != 0
	}
	account.Groups, err = d.nestedGroups(connection, entry.DN)
	if err != nil {
		return Account{}, err
	}
	return account, nil
}

// nestedGroups returns the objectGUIDs of every group the DN belongs to, directly or
// through other groups. It uses paging so a member of many groups isn't cut off.
func (d *LDAPDirectory) nestedGroups(connection *ldap.Conn, userDN string) ([]string, error) {
	groups, err := connection.SearchWithPaging(ldap.NewSearchRequest(
		d.config.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, int(d.config.Timeout.Seconds()), false,
		fmt.Sprintf("(&(objectClass=group)(member:%s:=%s))", nestedGroupsMatchingRule, ldap.EscapeFilter(userDN)),
		[]string{"objectGUID"},
		nil,
	), ldapPageSize)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]struct{}, len(groups.Entries))
	for _, group := range groups.Entries {
		raw := group.GetRawAttributeValue("objectGUID")
		if len(raw) == 0 {
			return nil, fmt.Errorf("group %q has no objectGUID", group.DN)
		}
		id, err := ObjectGUIDString(raw)
		if err != nil {
			return nil, fmt.Errorf("group %q: %w", group.DN, err)
		}
		ids[id] = struct{}{}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

// ldapPageSize stays below the default Active Directory MaxPageSize of 1000.
const ldapPageSize = 500

func (d *LDAPDirectory) connect(ctx context.Context, address string) (*ldap.Conn, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: d.config.InsecureSkipVerify}
	connection, err := ldap.DialURL(address,
		ldap.DialWithTLSConfig(tlsConfig),
		ldap.DialWithDialer(&net.Dialer{Timeout: d.config.Timeout}),
	)
	if err != nil {
		return nil, err
	}
	connection.SetTimeout(d.config.Timeout)
	if d.config.StartTLS {
		if err := connection.StartTLS(tlsConfig); err != nil {
			connection.Close()
			return nil, err
		}
	}
	if err := connection.Bind(d.config.BindDN, d.config.BindPassword); err != nil {
		connection.Close()
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		connection.SetTimeout(time.Until(deadline))
	}
	return connection, nil
}

func userFromEntry(entry *ldap.Entry, usernameAttribute string) User {
	displayName := entry.GetAttributeValue("displayName")
	if displayName == "" {
		displayName = entry.GetAttributeValue("cn")
	}
	id := ""
	if raw := entry.GetRawAttributeValue("objectGUID"); len(raw) != 0 {
		id, _ = ObjectGUIDString(raw)
	}
	return User{
		ID:                id,
		Username:          entry.GetAttributeValue(usernameAttribute),
		Email:             entry.GetAttributeValue("mail"),
		DisplayName:       displayName,
		DistinguishedName: entry.DN,
	}
}

// objectGUIDFilter turns the textual GUID back into AD's byte order and escapes every
// byte, because objectGUID is matched as binary.
func objectGUIDFilter(id string) (string, error) {
	compact := strings.ReplaceAll(strings.TrimSpace(id), "-", "")
	text, err := hex.DecodeString(compact)
	if err != nil || len(text) != 16 {
		return "", fmt.Errorf("objectGUID %q is invalid", id)
	}
	raw := []byte{text[3], text[2], text[1], text[0], text[5], text[4], text[7], text[6]}
	raw = append(raw, text[8:]...)
	var filter strings.Builder
	filter.WriteString("(objectGUID=")
	for _, value := range raw {
		fmt.Fprintf(&filter, "\\%02x", value)
	}
	filter.WriteString(")")
	return filter.String(), nil
}

// NormalizeGUID returns the canonical lowercase form, or "" if value isn't a GUID.
func NormalizeGUID(value string) string {
	compact := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))
	if len(compact) != 32 {
		return ""
	}
	if _, err := hex.DecodeString(compact); err != nil {
		return ""
	}
	return compact[0:8] + "-" + compact[8:12] + "-" + compact[12:16] + "-" + compact[16:20] + "-" + compact[20:32]
}

func (d *LDAPDirectory) FindUser(ctx context.Context, username string) (User, error) {
	if strings.TrimSpace(username) == "" {
		return User{}, fmt.Errorf("LDAP username is empty")
	}
	var failures []error
	for _, address := range d.config.URLs {
		user, err := d.findUserFromServer(ctx, address, username)
		if err == nil || errors.Is(err, ErrUserNotFound) {
			return user, err
		}
		failures = append(failures, fmt.Errorf("%s: %w", address, err))
	}
	return User{}, errors.Join(failures...)
}

func (d *LDAPDirectory) SearchUsers(ctx context.Context, query string) ([]User, error) {
	query = strings.TrimSpace(query)
	if len(query) < 2 {
		return nil, fmt.Errorf("LDAP search query must contain at least 2 characters")
	}
	var failures []error
	for _, address := range d.config.URLs {
		users, err := d.searchUsersOnServer(ctx, address, query)
		if err == nil {
			return users, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", address, err))
	}
	return nil, errors.Join(failures...)
}

func (d *LDAPDirectory) searchUsersOnServer(ctx context.Context, address, query string) ([]User, error) {
	connection, err := d.connect(ctx, address)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	result, err := connection.Search(ldap.NewSearchRequest(
		d.config.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, int(d.config.Timeout.Seconds()), false,
		fmt.Sprintf("(&(objectCategory=person)(objectClass=user)(%s=%s*))", d.config.UsernameAttribute, ldap.EscapeFilter(query)),
		[]string{d.config.UsernameAttribute, "mail", "displayName", "cn", "objectGUID"},
		[]ldap.Control{ldap.NewControlPaging(10)},
	))
	if err != nil {
		return nil, err
	}
	users := make([]User, 0, len(result.Entries))
	for _, entry := range result.Entries {
		user := userFromEntry(entry, d.config.UsernameAttribute)
		if user.Username == "" {
			continue
		}
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool {
		left, right := strings.ToLower(users[i].Username), strings.ToLower(users[j].Username)
		if left == right {
			return users[i].Username < users[j].Username
		}
		return left < right
	})
	return users, nil
}

func (d *LDAPDirectory) findUserFromServer(ctx context.Context, address, username string) (User, error) {
	connection, err := d.connect(ctx, address)
	if err != nil {
		return User{}, err
	}
	defer connection.Close()
	result, err := connection.Search(ldap.NewSearchRequest(
		d.config.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, int(d.config.Timeout.Seconds()), false,
		fmt.Sprintf("(&(objectCategory=person)(objectClass=user)(%s=%s))", d.config.UsernameAttribute, ldap.EscapeFilter(username)),
		[]string{d.config.UsernameAttribute, "mail", "displayName", "cn", "objectGUID"},
		nil,
	))
	if err != nil {
		return User{}, err
	}
	if len(result.Entries) == 0 {
		return User{}, ErrUserNotFound
	}
	if len(result.Entries) != 1 {
		return User{}, fmt.Errorf("expected one LDAP user, found %d", len(result.Entries))
	}
	return userFromEntry(result.Entries[0], d.config.UsernameAttribute), nil
}

func (d *LDAPDirectory) ListGroups(ctx context.Context) ([]Group, error) {
	var failures []error
	for _, address := range d.config.URLs {
		groups, err := d.listGroupsFromServer(ctx, address)
		if err == nil {
			return groups, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", address, err))
	}
	return nil, errors.Join(failures...)
}

func (d *LDAPDirectory) listGroupsFromServer(ctx context.Context, address string) ([]Group, error) {
	connection, err := d.connect(ctx, address)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	// Paged: a directory with more groups than MaxPageSize would otherwise answer
	// sizeLimitExceeded and break every group grant.
	result, err := connection.SearchWithPaging(ldap.NewSearchRequest(
		d.config.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, int(d.config.Timeout.Seconds()), false,
		"(objectClass=group)",
		[]string{"objectGUID", "cn"},
		nil,
	), ldapPageSize)
	if err != nil {
		return nil, err
	}
	groups := make([]Group, 0, len(result.Entries))
	for _, entry := range result.Entries {
		raw := entry.GetRawAttributeValue("objectGUID")
		if len(raw) == 0 {
			continue
		}
		id, err := ObjectGUIDString(raw)
		if err != nil {
			return nil, fmt.Errorf("group %q: %w", entry.DN, err)
		}
		name := entry.GetAttributeValue("cn")
		if name == "" {
			name = entry.DN
		}
		groups = append(groups, Group{ID: id, Name: name, DistinguishedName: entry.DN})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Name == groups[j].Name {
			return groups[i].ID < groups[j].ID
		}
		return groups[i].Name < groups[j].Name
	})
	return groups, nil
}

// ErrGroupNotFound means no group has the requested objectGUID.
var ErrGroupNotFound = errors.New("LDAP group not found")

// SearchGroups returns at most limit groups whose name starts with prefix, so a grant form
// can suggest groups without loading the whole directory.
func (d *LDAPDirectory) SearchGroups(ctx context.Context, prefix string, limit int) ([]Group, error) {
	prefix = strings.TrimSpace(prefix)
	if len(prefix) < 2 {
		return nil, fmt.Errorf("LDAP group search must contain at least 2 characters")
	}
	filter := fmt.Sprintf("(&(objectClass=group)(cn=%s*))", ldap.EscapeFilter(prefix))
	return d.groups(ctx, filter, limit)
}

// LookupGroup reads one group by objectGUID.
func (d *LDAPDirectory) LookupGroup(ctx context.Context, id string) (Group, error) {
	filter, err := objectGUIDFilter(id)
	if err != nil {
		return Group{}, ErrGroupNotFound
	}
	groups, err := d.groups(ctx, "(&(objectClass=group)"+filter+")", 2)
	if err != nil {
		return Group{}, err
	}
	if len(groups) == 0 {
		return Group{}, ErrGroupNotFound
	}
	return groups[0], nil
}

func (d *LDAPDirectory) groups(ctx context.Context, filter string, limit int) ([]Group, error) {
	var failures []error
	for _, address := range d.config.URLs {
		connection, err := d.connect(ctx, address)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", address, err))
			continue
		}
		result, err := connection.Search(ldap.NewSearchRequest(
			d.config.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, limit, int(d.config.Timeout.Seconds()), false,
			filter, []string{"objectGUID", "cn", "description"}, nil,
		))
		connection.Close()
		// A size limit is expected: limit caps the answer, not the directory.
		if result == nil || (err != nil && !ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded)) {
			failures = append(failures, fmt.Errorf("%s: %w", address, err))
			continue
		}
		groups := make([]Group, 0, len(result.Entries))
		for _, entry := range result.Entries {
			raw := entry.GetRawAttributeValue("objectGUID")
			id, err := ObjectGUIDString(raw)
			if err != nil {
				continue
			}
			name := entry.GetAttributeValue("cn")
			if name == "" {
				name = entry.DN
			}
			groups = append(groups, Group{ID: id, Name: name, DistinguishedName: entry.DN, Description: entry.GetAttributeValue("description")})
		}
		sort.Slice(groups, func(i, j int) bool { return strings.ToLower(groups[i].Name) < strings.ToLower(groups[j].Name) })
		return groups, nil
	}
	return nil, errors.Join(failures...)
}

func ObjectGUIDString(raw []byte) (string, error) {
	if len(raw) != 16 {
		return "", fmt.Errorf("objectGUID must contain 16 bytes, got %d", len(raw))
	}
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		raw[3], raw[2], raw[1], raw[0],
		raw[5], raw[4],
		raw[7], raw[6],
		raw[8], raw[9],
		raw[10], raw[11], raw[12], raw[13], raw[14], raw[15],
	), nil
}

func boolEnv(name string, fallback bool) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a Boolean", name)
	}
	return parsed, nil
}
