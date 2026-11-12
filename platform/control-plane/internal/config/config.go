package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Address               string
	GRPCAddress           string
	DatabaseURL           string
	StaticDir             string
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
}

func FromEnv() (Config, error) {
	config := Config{
		Address:               env("LORE_CP_ADDR", ":8080"),
		GRPCAddress:           env("LORE_GRPC_ADDR", ":9090"),
		DatabaseURL:           env("DATABASE_URL", "memory://"),
		StaticDir:             env("STATIC_DIR", ""),
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

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
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
