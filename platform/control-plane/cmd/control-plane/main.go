package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/oauth2/clientcredentials"

	"github.com/arutar/lore/platform/control-plane/internal/config"
	"github.com/arutar/lore/platform/control-plane/internal/httpapi"
	"github.com/arutar/lore/platform/control-plane/internal/loreclient"
	"github.com/arutar/lore/platform/control-plane/internal/loregrpc"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

func main() {
	if err := run(); err != nil {
		log.Printf("control plane stopped: %v", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	settings, err := config.FromEnv()
	if err != nil {
		return err
	}

	var storage store.Store
	if settings.DatabaseURL == "memory://" {
		storage = store.NewMemory()
		log.Print("using non-durable in-memory storage")
	} else {
		storage, err = store.NewPostgres(ctx, settings.DatabaseURL)
		if err != nil {
			return err
		}
	}
	defer func() { _ = storage.Close() }()

	auth, err := httpapi.NewAuthenticator(ctx, storage, httpapi.AuthConfig{
		Issuer:         settings.OIDCIssuer,
		InternalIssuer: settings.OIDCInternalIssuer,
		ClientID:       settings.OIDCClientID,
		ClientSecret:   settings.OIDCClientSecret,
		RedirectURL:    settings.OIDCRedirectURL,
		SecureCookies:  settings.SecureCookies,
		SessionTTL:     settings.SessionTTL,
		BootstrapAdmin: settings.BootstrapAdmin,
	})
	if err != nil {
		return err
	}
	var repositories httpapi.RepositoryCreator
	if settings.LoreRepositoryAddress != "" {
		oauth := clientcredentials.Config{
			ClientID: settings.LoreServiceClientID, ClientSecret: settings.LoreServiceSecret, TokenURL: settings.LoreServiceTokenURL,
		}
		tokens := oauth.TokenSource(ctx)
		client, err := loreclient.New(loreclient.Config{Address: settings.LoreRepositoryAddress, TokenSource: tokens, Secure: settings.LoreGRPCSecure})
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		repositories = client
	}
	var exchange *httpapi.TokenExchange
	if settings.TokenExchangeIssuer != "" {
		exchange, err = httpapi.NewTokenExchange(storage, auth, settings.TokenExchangeIssuer, "lore-native", settings.TokenSigningKeyFile)
		if err != nil {
			return err
		}
		auth.SetTokenExchange(exchange)
		if settings.TokenSigningKeyFile == "" {
			log.Print("token exchange uses an ephemeral development signing key")
		}
	}
	handler := httpapi.NewServer(storage, auth, httpapi.Config{
		Repositories:  repositories,
		TokenExchange: exchange,
		StaticDir:     settings.StaticDir,
		DevAuth:       settings.DevAuth,
		DevUser:       model.User{ID: settings.DevUserID, Username: settings.DevUsername, DisplayName: settings.DevUsername, Role: model.RoleAdmin, Source: "local"},
		OIDC:          httpapi.AuthConfig{},
		ExternalURL:   settings.ExternalURL,
	})

	server := &http.Server{
		Addr:              settings.Address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	grpcListener, err := net.Listen("tcp", settings.GRPCAddress)
	if err != nil {
		return err
	}
	grpcServer := loregrpc.NewServer(storage, auth)
	serveError := make(chan error, 2)
	go func() {
		log.Printf("HTTP listening on %s", settings.Address)
		serveError <- server.ListenAndServe()
	}()
	go func() {
		log.Printf("Lore gRPC listening on %s", settings.GRPCAddress)
		serveError <- grpcServer.Serve(grpcListener)
	}()

	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-serveError:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	grpcServer.Stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(serveErr, server.Shutdown(shutdownCtx))
}
