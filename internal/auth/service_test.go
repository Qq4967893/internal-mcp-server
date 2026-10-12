package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddlewareAcceptsDelegatedIdentityAndScopes(t *testing.T) {
	var identity Identity
	handler := Middleware("service-token", nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, _ = FromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer service-token")
	request.Header.Set(userIDHeader, "user-1")
	request.Header.Set(sessionIDHeader, "session-1")
	request.Header.Set(requestIDHeader, "request-1")
	request.Header.Set(scopeHeader, "admin.api.ping.read, admin.api.users.read")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if identity.UserID != "user-1" || identity.SessionID != "session-1" || identity.RequestID != "request-1" {
		t.Errorf("identity = %+v", identity)
	}
	if len(identity.Permissions) != 2 || identity.Permissions[1] != "admin.api.users.read" {
		t.Errorf("permissions = %v", identity.Permissions)
	}
}

func TestMiddlewareRejectsInvalidIdentity(t *testing.T) {
	handler := Middleware("service-token", nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer service-token")
	request.Header.Set(userIDHeader, "../secret")
	request.Header.Set(sessionIDHeader, "session")
	request.Header.Set(requestIDHeader, "request")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestDefaultAuthorizerChecksConfiguredPermission(t *testing.T) {
	authorizer := DefaultAuthorizer{}
	allowed := Identity{ServiceID: serviceID, Permissions: []string{"admin.api.ping.read"}}
	if err := authorizer.Authorize(allowed, "admin.api.ping.read"); err != nil {
		t.Fatalf("authorize allowed: %v", err)
	}
	if err := authorizer.Authorize(allowed, "admin.api.users.read"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("authorize forbidden: %v", err)
	}
}
