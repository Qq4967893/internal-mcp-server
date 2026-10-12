package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Global-MongoX/internal-mcp-server/internal/adapters/adminapi"
	"github.com/Global-MongoX/internal-mcp-server/internal/auth"
	"github.com/Global-MongoX/internal-mcp-server/internal/navigation"
	"github.com/Global-MongoX/internal-mcp-server/internal/registry"
	"github.com/Global-MongoX/internal-mcp-server/internal/toolruntime"
)

type Deps struct {
	Logger     *slog.Logger
	Registry   *registry.Registry
	Authorizer auth.Authorizer
	AdminAPI   *adminapi.Client
	Navigation *navigation.Manifest
	Timeout    time.Duration
}

func Handler(deps Deps) (http.Handler, error) {
	if deps.Logger == nil || deps.Registry == nil || deps.Authorizer == nil ||
		deps.AdminAPI == nil || deps.Navigation == nil {
		return nil, errors.New("mcpserver dependencies are incomplete")
	}
	if deps.Timeout <= 0 {
		deps.Timeout = 5 * time.Second
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "internal-mcp-server",
		Version: "0.1.0",
	}, &mcp.ServerOptions{
		Instructions: "Read-only admin-api access for AI Copilot. Tool failures are returned as structured ERROR envelopes.",
		Logger:       deps.Logger,
	})

	// Middleware is applied in reverse, so the first argument is the outermost
	// wrapper: audit must precede authorization to record rejected calls.
	server.AddReceivingMiddleware(auditMiddleware(deps.Logger), authorizationMiddleware(deps.Registry, deps.Authorizer))
	registerPingTool(server, deps)
	registerBusinessTools(server, deps)
	registerNavigationTools(server, deps)

	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		Logger:       deps.Logger,
	}), nil
}

type pingInput struct{}

type pingData struct {
	Status         string `json:"status"`
	UpstreamStatus int    `json:"upstream_http_status"`
	UpstreamReqID  string `json:"upstream_request_id"`
}

func registerPingTool(server *mcp.Server, deps Deps) {
	definition := registry.Definition{
		Name:                "admin_api_ping",
		Title:               "Admin API Health Ping",
		Description:         "Checks that admin-api is reachable through the configured base URL. This is a connectivity probe, not a business data tool.",
		Version:             "1.0.0",
		Owner:               "ai-copilot-platform",
		RequiredPermissions: []string{"admin.api.ping.read"},
		SensitivityLevel:    "public_internal",
		Timeout:             deps.Timeout.String(),
		AuditPolicy:         "metadata_only",
	}
	if err := deps.Registry.Register(definition); err != nil {
		panic(err)
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        definition.Name,
		Title:       definition.Title,
		Description: definition.Description,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ pingInput) (*mcp.CallToolResult, toolruntime.Envelope[pingData], error) {
		toolCtx, cancel := context.WithTimeout(ctx, deps.Timeout)
		defer cancel()

		health, err := deps.AdminAPI.Health(toolCtx)
		source := toolruntime.Source{
			Service:    "admin-api",
			Resource:   "health",
			ObservedAt: time.Now().UTC(),
		}
		if err != nil {
			return nil, errorEnvelope(err), nil
		}

		return nil, toolruntime.Success(pingData{
			Status:         health.Status,
			UpstreamStatus: health.HTTPStatus,
			UpstreamReqID:  health.RequestID,
		}, source), nil
	})
}

func errorEnvelope(err error) toolruntime.Envelope[pingData] {
	var upstream *adminapi.Error
	if errors.As(err, &upstream) {
		return toolruntime.Failure[pingData](string(upstream.Kind), upstream.Message, upstream.Retryable())
	}
	return toolruntime.Failure[pingData]("UPSTREAM_ERROR", "admin-api request failed", true)
}

func authorizationMiddleware(registry *registry.Registry, authorizer auth.Authorizer) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, request)
			}

			params, ok := request.GetParams().(*mcp.CallToolParamsRaw)
			if !ok {
				return nil, errors.New("invalid tool call parameters")
			}
			definition, exists := registry.Get(params.Name)
			if !exists {
				return nil, fmt.Errorf("tool %q is not registered", params.Name)
			}
			identity, ok := auth.FromContext(ctx)
			if !ok {
				return nil, auth.ErrUnauthenticated
			}
			for _, permission := range definition.RequiredPermissions {
				if err := authorizer.Authorize(identity, permission); err != nil {
					return nil, fmt.Errorf("tool %q authorization failed: %w", params.Name, err)
				}
			}
			return next(ctx, method, request)
		}
	}
}

func auditMiddleware(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			started := time.Now()
			identity, _ := auth.FromContext(ctx)
			toolName := ""
			if params, ok := request.GetParams().(*mcp.CallToolParamsRaw); ok {
				toolName = params.Name
			}

			result, err := next(ctx, method, request)
			logger.InfoContext(ctx, "mcp request audited",
				"method", method,
				"tool", toolName,
				"user_id", identity.UserID,
				"session_id", identity.SessionID,
				"request_id", identity.RequestID,
				"duration_ms", time.Since(started).Milliseconds(),
				"error", errorString(err),
			)
			return result, err
		}
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
