package directory

import (
	"context"
	"crypto/tls"
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
}

type User struct {
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

func (d *LDAPDirectory) GroupIDs(ctx context.Context, username string) ([]string, error) {
	if strings.TrimSpace(username) == "" {
		return nil, fmt.Errorf("LDAP username is empty")
	}
	var failures []error
	for _, address := range d.config.URLs {
		groups, err := d.groupIDsFromServer(ctx, address, username)
		if err == nil {
			return groups, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", address, err))
	}
	return nil, errors.Join(failures...)
}

func (d *LDAPDirectory) FindUser(ctx context.Context, username string) (User, error) {
	if strings.TrimSpace(username) == "" {
		return User{}, fmt.Errorf("LDAP username is empty")
	}
	var failures []error
	for _, address := range d.config.URLs {
		user, err := d.findUserFromServer(ctx, address, username)
		if err == nil {
			return user, nil
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
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: d.config.InsecureSkipVerify}
	connection, err := ldap.DialURL(address,
		ldap.DialWithTLSConfig(tlsConfig),
		ldap.DialWithDialer(&net.Dialer{Timeout: d.config.Timeout}),
	)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	connection.SetTimeout(d.config.Timeout)
	if d.config.StartTLS {
		if err := connection.StartTLS(tlsConfig); err != nil {
			return nil, err
		}
	}
	if err := connection.Bind(d.config.BindDN, d.config.BindPassword); err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		connection.SetTimeout(time.Until(deadline))
	}
	result, err := connection.Search(ldap.NewSearchRequest(
		d.config.BaseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		int(d.config.Timeout.Seconds()),
		false,
		fmt.Sprintf("(&(objectCategory=person)(%s=%s*))", d.config.UsernameAttribute, ldap.EscapeFilter(query)),
		[]string{d.config.UsernameAttribute, "mail", "displayName", "cn"},
		[]ldap.Control{ldap.NewControlPaging(10)},
	))
	if err != nil {
		return nil, err
	}
	users := make([]User, 0, len(result.Entries))
	for _, entry := range result.Entries {
		username := entry.GetAttributeValue(d.config.UsernameAttribute)
		if username == "" {
			continue
		}
		displayName := entry.GetAttributeValue("displayName")
		if displayName == "" {
			displayName = entry.GetAttributeValue("cn")
		}
		users = append(users, User{
			Username:    username,
			Email:       entry.GetAttributeValue("mail"),
			DisplayName: displayName,
		})
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
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: d.config.InsecureSkipVerify}
	connection, err := ldap.DialURL(address,
		ldap.DialWithTLSConfig(tlsConfig),
		ldap.DialWithDialer(&net.Dialer{Timeout: d.config.Timeout}),
	)
	if err != nil {
		return User{}, err
	}
	defer connection.Close()
	connection.SetTimeout(d.config.Timeout)
	if d.config.StartTLS {
		if err := connection.StartTLS(tlsConfig); err != nil {
			return User{}, err
		}
	}
	if err := connection.Bind(d.config.BindDN, d.config.BindPassword); err != nil {
		return User{}, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		connection.SetTimeout(time.Until(deadline))
	}
	result, err := connection.Search(ldap.NewSearchRequest(
		d.config.BaseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		1,
		int(d.config.Timeout.Seconds()),
		false,
		fmt.Sprintf("(%s=%s)", d.config.UsernameAttribute, ldap.EscapeFilter(username)),
		[]string{d.config.UsernameAttribute, "mail", "displayName", "cn"},
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
	entry := result.Entries[0]
	displayName := entry.GetAttributeValue("displayName")
	if displayName == "" {
		displayName = entry.GetAttributeValue("cn")
	}
	return User{
		Username:          entry.GetAttributeValue(d.config.UsernameAttribute),
		Email:             entry.GetAttributeValue("mail"),
		DisplayName:       displayName,
		DistinguishedName: entry.DN,
	}, nil
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
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: d.config.InsecureSkipVerify}
	connection, err := ldap.DialURL(address,
		ldap.DialWithTLSConfig(tlsConfig),
		ldap.DialWithDialer(&net.Dialer{Timeout: d.config.Timeout}),
	)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	connection.SetTimeout(d.config.Timeout)
	if d.config.StartTLS {
		if err := connection.StartTLS(tlsConfig); err != nil {
			return nil, err
		}
	}
	if err := connection.Bind(d.config.BindDN, d.config.BindPassword); err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		connection.SetTimeout(time.Until(deadline))
	}
	result, err := connection.Search(ldap.NewSearchRequest(
		d.config.BaseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		int(d.config.Timeout.Seconds()),
		false,
		"(objectClass=group)",
		[]string{"objectGUID", "cn"},
		nil,
	))
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

func (d *LDAPDirectory) groupIDsFromServer(ctx context.Context, address, username string) ([]string, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: d.config.InsecureSkipVerify}
	connection, err := ldap.DialURL(address,
		ldap.DialWithTLSConfig(tlsConfig),
		ldap.DialWithDialer(&net.Dialer{Timeout: d.config.Timeout}),
	)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	connection.SetTimeout(d.config.Timeout)
	if d.config.StartTLS {
		if err := connection.StartTLS(tlsConfig); err != nil {
			return nil, err
		}
	}
	if err := connection.Bind(d.config.BindDN, d.config.BindPassword); err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		connection.SetTimeout(time.Until(deadline))
	}
	users, err := connection.Search(ldap.NewSearchRequest(
		d.config.BaseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		1,
		int(d.config.Timeout.Seconds()),
		false,
		fmt.Sprintf("(sAMAccountName=%s)", ldap.EscapeFilter(username)),
		[]string{"distinguishedName"},
		nil,
	))
	if err != nil {
		return nil, err
	}
	if len(users.Entries) == 0 {
		return nil, ErrUserNotFound
	}
	if len(users.Entries) != 1 {
		return nil, fmt.Errorf("expected one LDAP user, found %d", len(users.Entries))
	}
	userDN := users.Entries[0].DN
	groups, err := connection.Search(ldap.NewSearchRequest(
		d.config.BaseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		int(d.config.Timeout.Seconds()),
		false,
		fmt.Sprintf("(&(objectClass=group)(member:%s:=%s))", nestedGroupsMatchingRule, ldap.EscapeFilter(userDN)),
		[]string{"objectGUID"},
		nil,
	))
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
