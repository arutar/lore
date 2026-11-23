package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Address               string
	GRPCAddress           string
	DatabaseURL           string
	StaticDir             string
	DownloadsDir          string
	ExternalURL           string
	NativeRepositoryURL   string
	DevAuth               bool
	DevUserID             string
	DevUsername           string
	SecureCookies         bool
	SessionTTL            time.Duration
	OIDCIssuer            string
	OIDCInternalIssuer    string
	OIDCClientID          string
	OIDCClientSecret      string
	OIDCRedirectURL       string
	BootstrapAdmin        string
	LoreRepositoryAddress string
	LoreServiceTokenURL   string
	LoreServiceClientID   string
	LoreServiceSecret     string
	LoreGRPCSecure        bool
	TokenExchangeIssuer   string
	TokenSigningKeyFile   string
	DirectoryRefresh      time.Duration
	DirectoryOutageGrace  time.Duration
	// MCP is off, internal (only from MCPNetworks) or on.
	MCP            string
	MCPNetworks    []netip.Prefix
	MCPConcurrency int
	MCPMaxRead     int
	MCPLinkTTL     time.Duration
}

func FromEnv() (Config, error) {
	config := Config{
		Address:               env("LORE_CP_ADDR", ":8080"),
		GRPCAddress:           env("LORE_GRPC_ADDR", ":9090"),
		DatabaseURL:           env("DATABASE_URL", "memory://"),
		StaticDir:             env("STATIC_DIR", ""),
		DownloadsDir:          os.Getenv("DOWNLOADS_DIR"),
		ExternalURL:           env("EXTERNAL_URL", "http://localhost:8080"),
		NativeRepositoryURL:   os.Getenv("LORE_NATIVE_PUBLIC_URL"),
		DevUserID:             env("DEV_USER_ID", "dev-admin"),
		DevUsername:           env("DEV_USERNAME", "admin"),
		SessionTTL:            8 * time.Hour,
		OIDCIssuer:            os.Getenv("OIDC_ISSUER"),
		OIDCInternalIssuer:    os.Getenv("OIDC_INTERNAL_ISSUER"),
		OIDCClientID:          os.Getenv("OIDC_CLIENT_ID"),
		OIDCClientSecret:      os.Getenv("OIDC_CLIENT_SECRET"),
		OIDCRedirectURL:       os.Getenv("OIDC_REDIRECT_URL"),
		BootstrapAdmin:        os.Getenv("BOOTSTRAP_ADMIN"),
		LoreRepositoryAddress: os.Getenv("LORE_REPOSITORY_ADDR"),
		LoreServiceTokenURL:   os.Getenv("LORE_SERVICE_TOKEN_URL"),
		LoreServiceClientID:   os.Getenv("LORE_SERVICE_CLIENT_ID"),
		LoreServiceSecret:     os.Getenv("LORE_SERVICE_CLIENT_SECRET"),
		TokenExchangeIssuer:   os.Getenv("LORE_TOKEN_EXCHANGE_ISSUER"),
		TokenSigningKeyFile:   os.Getenv("LORE_TOKEN_SIGNING_KEY_FILE"),
	}
	var err error
	config.DevAuth, err = boolEnv("DEV_AUTH_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	config.SecureCookies, err = boolEnv("SECURE_COOKIES", !config.DevAuth)
	if err != nil {
		return Config{}, err
	}
	config.LoreGRPCSecure, err = boolEnv("LORE_GRPC_SECURE", true)
	if err != nil {
		return Config{}, err
	}
	if value := os.Getenv("SESSION_TTL"); value != "" {
		config.SessionTTL, err = time.ParseDuration(value)
		if err != nil || config.SessionTTL < time.Minute || config.SessionTTL > 30*24*time.Hour {
			return Config{}, fmt.Errorf("SESSION_TTL must be between 1m and 720h")
		}
	}
	config.DirectoryRefresh, err = durationEnv("LORE_LDAP_REFRESH", 5*time.Minute, 30*time.Second, 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	config.DirectoryOutageGrace, err = durationEnv("LORE_LDAP_OUTAGE_GRACE", 8*time.Hour, 0, 7*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	if err := mcpFromEnv(&config); err != nil {
		return Config{}, err
	}
	if config.OIDCRedirectURL == "" {
		config.OIDCRedirectURL = config.ExternalURL + "/web/v1/oidc/callback"
	}
	if !config.DevAuth && config.OIDCIssuer == "" {
		return Config{}, fmt.Errorf("OIDC_ISSUER is required unless DEV_AUTH_ENABLED=true")
	}
	if config.OIDCIssuer != "" && config.OIDCClientID == "" {
		return Config{}, fmt.Errorf("OIDC_CLIENT_ID is required when OIDC_ISSUER is set")
	}
	if config.LoreRepositoryAddress != "" && (config.LoreServiceTokenURL == "" || config.LoreServiceClientID == "" || config.LoreServiceSecret == "") {
		return Config{}, fmt.Errorf("LORE_SERVICE_TOKEN_URL, LORE_SERVICE_CLIENT_ID and LORE_SERVICE_CLIENT_SECRET are required with LORE_REPOSITORY_ADDR")
	}
	return config, nil
}

// mcpFromEnv reads the MCP settings: LORE_MCP (off, internal or on), LORE_MCP_NETWORKS (the
// networks internal answers, as CIDRs separated by ";", "," or spaces), LORE_MCP_CONCURRENCY
// (calls a token may run at once, 0 for no limit), LORE_MCP_MAX_READ (the most bytes read_text
// returns in one call) and LORE_MCP_LINK_TTL (how long a download link works).
func mcpFromEnv(config *Config) error {
	config.MCP = strings.ToLower(env("LORE_MCP", "off"))
	switch config.MCP {
	case "off", "internal", "on":
	default:
		return fmt.Errorf("LORE_MCP must be off, internal or on")
	}
	for _, value := range strings.FieldsFunc(os.Getenv("LORE_MCP_NETWORKS"), func(r rune) bool { return r == ';' || r == ',' || r == ' ' || r == '\t' }) {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			address, addressErr := netip.ParseAddr(value)
			if addressErr != nil {
				return fmt.Errorf("LORE_MCP_NETWORKS: %q isn't a network such as 10.0.0.0/16", value)
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		config.MCPNetworks = append(config.MCPNetworks, prefix.Masked())
	}
	if config.MCP == "internal" && len(config.MCPNetworks) == 0 {
		return fmt.Errorf("LORE_MCP=internal needs LORE_MCP_NETWORKS, for example 10.0.0.0/16;192.168.0.0/24")
	}
	var err error
	if config.MCPConcurrency, err = intEnv("LORE_MCP_CONCURRENCY", 8, 0, 1000); err != nil {
		return err
	}
	if config.MCPMaxRead, err = intEnv("LORE_MCP_MAX_READ", 1<<20, 4<<10, 64<<20); err != nil {
		return err
	}
	config.MCPLinkTTL, err = durationEnv("LORE_MCP_LINK_TTL", 10*time.Minute, time.Minute, 24*time.Hour)
	return err
}

func intEnv(name string, fallback, minimum, maximum int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be a whole number between %d and %d", name, minimum, maximum)
	}
	return parsed, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func durationEnv(name string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be a duration between %s and %s", name, minimum, maximum)
	}
	return parsed, nil
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
