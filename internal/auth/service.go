package auth

import (
	"crypto/subtle"
	"net/http"
	"regexp"
	"strings"
)

const (
	serviceID = "ai-copilot-api"

	userIDHeader    = "X-Copilot-User-Id"
	sessionIDHeader = "X-Copilot-Session-Id"
	requestIDHeader = "X-Copilot-Request-Id"
	scopeHeader     = "X-Copilot-Scopes"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,128}$`)

type Authorizer interface {
	Authorize(identity Identity, permission string) error
}

type DefaultAuthorizer struct{}

func (DefaultAuthorizer) Authorize(identity Identity, permission string) error {
	if identity.ServiceID != serviceID {
		return ErrForbidden
	}
	if permission != "" && !identity.HasPermission(permission) {
		return ErrForbidden
	}
	return nil
}

func Middleware(serviceToken string, defaultPermissions []string, next http.Handler) http.Handler {
	expected := []byte(serviceToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r.Header.Get("Authorization"))
		if token == "" || subtle.ConstantTimeCompare([]byte(token), expected) != 1 {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing or invalid service token")
			return
		}

		userID := r.Header.Get(userIDHeader)
		sessionID := r.Header.Get(sessionIDHeader)
		requestID := r.Header.Get(requestIDHeader)
		if !identifierPattern.MatchString(userID) || !identifierPattern.MatchString(sessionID) || !identifierPattern.MatchString(requestID) {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid delegated identity headers")
			return
		}

		permissions := defaultPermissions
		if scopes := r.Header.Get(scopeHeader); scopes != "" {
			permissions = splitAndTrim(scopes)
		}

		ctx := WithIdentity(r.Context(), Identity{
			ServiceID:   serviceID,
			UserID:      userID,
			SessionID:   sessionID,
			RequestID:   requestID,
			Permissions: permissions,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(value string) string {
	const scheme = "Bearer "
	if len(value) <= len(schema()) || !strings.EqualFold(value[:len(scheme)], scheme) {
		return ""
	}
	token := strings.TrimSpace(value[len(scheme):])
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return ""
	}
	return token
}

func schema() string { return "Bearer " }

func splitAndTrim(value string) []string {
	fields := strings.Split(value, ",")
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
