package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Global-MongoX/internal-mcp-server/internal/adapters/adminapi"
	"github.com/Global-MongoX/internal-mcp-server/internal/auth"
	"github.com/Global-MongoX/internal-mcp-server/internal/config"
	"github.com/Global-MongoX/internal-mcp-server/internal/ratelimit"
)

// maxLoggedBadRequestBody keeps diagnostic logs useful without allowing an
// unexpectedly large response body to consume log storage.
const maxLoggedBadRequestBody = 16 << 10

type Server struct {
	cfg      config.Config
	logger   *slog.Logger
	mcp      http.Handler
	adminAPI *adminapi.Client
	limiter  *ratelimit.Limiter
}

func New(cfg config.Config, logger *slog.Logger, mcpHandler http.Handler, adminClient *adminapi.Client) *Server {
	return &Server{
		cfg:      cfg,
		logger:   logger,
		mcp:      mcpHandler,
		adminAPI: adminClient,
		limiter:  ratelimit.New(cfg.RateLimit.RequestsPerMinute, cfg.RateLimit.Burst),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.Handle("/mcp", s.secure(s.mcp))

	return s.recoverPanics(s.logRequests(mux))
}

func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Middleware(s.cfg.Auth.ServiceToken, s.cfg.Auth.DefaultPermissions, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := auth.FromContext(r.Context())
			if ok {
				if !s.limiter.Allow("service:" + identity.ServiceID + ":user:" + identity.UserID) {
					writeJSON(w, http.StatusTooManyRequests, map[string]any{
						"error": map[string]string{
							"code":    "RATE_LIMITED",
							"message": "too many requests",
						},
					})
					return
				}
			}
			next.ServeHTTP(w, r)
		})).ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	if _, err := s.adminAPI.Health(ctx); err != nil {
		s.logger.WarnContext(ctx, "readiness check failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unavailable",
			"error":  "admin-api is unavailable",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		requestID := r.Header.Get("X-Copilot-Request-Id")
		if requestID == "" {
			requestID = newRequestID()
		}
		r.Header.Set("X-Copilot-Request-Id", requestID)
		w.Header().Set("X-Copilot-Request-Id", requestID)

		next.ServeHTTP(recorder, r)
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration_ms", time.Since(started).Milliseconds(),
			"request_id", requestID,
		}
		// MCP SDK puts the actionable protocol-validation reason in the HTTP
		// 400 body (for example an unsupported protocol version or malformed
		// JSON-RPC request). Do not log request bodies or non-400 responses.
		if recorder.status == http.StatusBadRequest {
			attrs = append(attrs, "response_body", recorder.body.String())
		}
		s.logger.InfoContext(r.Context(), "http request", attrs...)
	})
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("http handler panic",
					"panic", recovered,
					"stack", string(debug.Stack()),
					"request_id", r.Header.Get("X-Copilot-Request-Id"),
				)
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": map[string]string{
						"code":    "INTERNAL",
						"message": "internal server error",
					},
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(data []byte) (int, error) {
	if r.body.Len() < maxLoggedBadRequestBody {
		remaining := maxLoggedBadRequestBody - r.body.Len()
		_, _ = r.body.Write(data[:min(len(data), remaining)])
	}
	return r.ResponseWriter.Write(data)
}

func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func newRequestID() string {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		return strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "-")
	}
	return hex.EncodeToString(data[:])
}
