package adminapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Global-MongoX/internal-mcp-server/internal/auth"
)

func newTestClient(t *testing.T, server *httptest.Server, timeout time.Duration) *Client {
	t.Helper()
	client, err := NewClient(server.URL, timeout, NewStaticCredentialProvider(Credentials{
		Cookies: map[string]string{"session_id": "test-session", "XSRF-TOKEN": "test-xsrf"},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestHealthSendsCredentialsAndIdentity(t *testing.T) {
	var authorization, cookie, xsrf, userID, requestID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		cookie = r.Header.Get("Cookie")
		xsrf = r.Header.Get("X-XSRF-TOKEN")
		userID = r.Header.Get("X-Copilot-User-Id")
		requestID = r.Header.Get("X-Copilot-Request-Id")
		w.Header().Set("X-Request-Id", "upstream-request")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))
	defer server.Close()

	ctx := auth.WithIdentity(context.Background(), auth.Identity{
		UserID: "user-1", SessionID: "session-1", RequestID: "request-1",
	})
	health, err := newTestClient(t, server, time.Second).Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != "ok" || health.HTTPStatus != http.StatusOK || health.RequestID != "upstream-request" {
		t.Errorf("health = %+v", health)
	}
	// Cookies are sorted by name, and the XSRF token rides in the Cookie header
	// rather than in an X-XSRF-TOKEN header of its own.
	if cookie != "XSRF-TOKEN=test-xsrf; session_id=test-session" {
		t.Errorf("cookie = %q", cookie)
	}
	if xsrf != "" {
		t.Errorf("X-XSRF-TOKEN = %q, want none", xsrf)
	}
	if authorization != "" {
		t.Errorf("authorization = %q, want none", authorization)
	}
	if userID != "user-1" || requestID != "request-1" {
		t.Errorf("identity: user=%q request=%q", userID, requestID)
	}
}

func TestHealthAcceptsAnyStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "ok", status: http.StatusOK, body: "OK"},
		{name: "failed envelope", status: http.StatusOK, body: `{"is_succ":false,"code":422,"message":"bad input","request_id":"req"}`},
		{name: "login required", status: http.StatusUnauthorized, body: `{"is_succ":false,"code":10000,"message":"login required","request_id":"req"}`},
		{name: "rate limit", status: http.StatusTooManyRequests, body: "rate"},
		{name: "server error", status: http.StatusInternalServerError, body: "boom"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			health, err := newTestClient(t, server, time.Second).Health(context.Background())
			if err != nil {
				t.Fatalf("health = %T, %v", err, err)
			}
			if health.Status != "ok" || health.HTTPStatus != test.status {
				t.Errorf("health = %+v", health)
			}
		})
	}
}

func TestHealthMapsTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	_, err := newTestClient(t, server, time.Millisecond).Health(context.Background())
	var adminErr *Error
	if !errors.As(err, &adminErr) || adminErr.Kind != ErrorKindTimeout {
		t.Fatalf("error = %T, %v", err, err)
	}
}

func TestHealthAcceptsBodiesWithoutEnvelopeFields(t *testing.T) {
	// A 200 whose body carries neither is_succ nor code is not an envelope, so
	// it must not be read as a failed envelope.
	bodies := []string{"OK", `{"status":"ok"}`, `{}`, "null", `{"message":"up"}`, `{"is_succ":true,"code":0}`}

	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			health, err := newTestClient(t, server, time.Second).Health(context.Background())
			if err != nil {
				t.Fatalf("health failed for body %s: %v", body, err)
			}
			if health.Status != "ok" || health.HTTPStatus != http.StatusOK {
				t.Errorf("health = %+v", health)
			}
		})
	}
}

func TestHealthUsesTemporaryPermissionsRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/managers/permissions" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"is_succ":false,"code":500,"message":"broken","request_id":"req"}`))
	}))
	defer server.Close()

	health, err := newTestClient(t, server, time.Second).Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != "ok" {
		t.Errorf("health = %+v", health)
	}
}

func TestErrorRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want bool
	}{
		{name: "timeout", err: &Error{Kind: ErrorKindTimeout}, want: true},
		{name: "rate limited", err: &Error{Kind: ErrorKindRateLimited, HTTPStatus: http.StatusTooManyRequests}, want: true},
		{name: "server error", err: &Error{Kind: ErrorKindUpstream, HTTPStatus: http.StatusInternalServerError}, want: true},
		{name: "bad gateway", err: &Error{Kind: ErrorKindUpstream, HTTPStatus: http.StatusBadGateway}, want: true},
		{name: "transport failure", err: &Error{Kind: ErrorKindUpstream, Cause: errors.New("connection reset")}, want: true},
		{name: "not found", err: &Error{Kind: ErrorKindUpstream, HTTPStatus: http.StatusNotFound}, want: false},
		{name: "bad request", err: &Error{Kind: ErrorKindUpstream, HTTPStatus: http.StatusBadRequest}, want: false},
		{name: "method not allowed", err: &Error{Kind: ErrorKindUpstream, HTTPStatus: http.StatusMethodNotAllowed}, want: false},
		{name: "forbidden", err: &Error{Kind: ErrorKindForbidden, HTTPStatus: http.StatusForbidden}, want: false},
		{name: "invalid", err: &Error{Kind: ErrorKindInvalid, HTTPStatus: http.StatusOK}, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.err.Retryable(); got != test.want {
				t.Errorf("Retryable() = %t, want %t", got, test.want)
			}
		})
	}
}
