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
