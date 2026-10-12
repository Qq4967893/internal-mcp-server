package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Global-MongoX/internal-mcp-server/internal/adapters/adminapi"
	"github.com/Global-MongoX/internal-mcp-server/internal/auth"
	"github.com/Global-MongoX/internal-mcp-server/internal/config"
	"github.com/Global-MongoX/internal-mcp-server/internal/httpapi"
	"github.com/Global-MongoX/internal-mcp-server/internal/mcpserver"
	"github.com/Global-MongoX/internal-mcp-server/internal/navigation"
	"github.com/Global-MongoX/internal-mcp-server/internal/observability"
	"github.com/Global-MongoX/internal-mcp-server/internal/registry"
	navdata "github.com/Global-MongoX/internal-mcp-server/knowledge/navigation"
)

func main() {
	configPath := flag.String("config", "", "path to JSON config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	logger := observability.NewLogger(cfg.LogLevel)

	credentials := adminapi.NewStaticCredentialProvider(adminapi.Credentials{
		Cookies: cfg.AdminAPI.Credentials.Cookies,
	})
	adminClient, err := adminapi.NewClient(
		cfg.AdminAPI.BaseURL,
		cfg.AdminAPI.Timeout.Duration(),
		credentials,
		nil,
	)
	if err != nil {
		logger.Error("initialize admin-api client", "error", err)
		os.Exit(1)
	}

	// A malformed manifest must stop the process: every navigation answer
	// would otherwise be a console link built from data nobody checked.
	navigationManifest, err := navigation.Load(navdata.PagesYAML, cfg.AdminConsole.BaseURL)
	if err != nil {
		logger.Error("load navigation manifest", "error", err)
		os.Exit(1)
	}

	mcpHandler, err := mcpserver.Handler(mcpserver.Deps{
		Logger:     logger,
		Registry:   registry.New(),
		Authorizer: auth.DefaultAuthorizer{},
		AdminAPI:   adminClient,
		Navigation: navigationManifest,
		Timeout:    cfg.AdminAPI.Timeout.Duration(),
	})
	if err != nil {
		logger.Error("initialize MCP server", "error", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           httpapi.New(cfg, logger, mcpHandler, adminClient).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("internal-mcp-server listening", "addr", cfg.Server.Addr)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("internal-mcp-server stopped")
}
