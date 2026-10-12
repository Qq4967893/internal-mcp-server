package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Global-MongoX/internal-mcp-server/internal/auth"
)

const maxResponseBytes = 1 << 20

type Envelope struct {
	IsSucc    bool   `json:"is_succ"`
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data"`
	RequestID string `json:"request_id"`

	// hasIsSucc records whether the body carried an is_succ key, so Succeeded
	// can tell an explicit false from an envelope that only carried code.
	hasIsSucc bool
}

// Succeeded reports whether the envelope describes a successful call. An
// envelope needs only one of is_succ or code to be present (see
// decodeEnvelope), so a body carrying code alone succeeds when that code is 0;
// is_succ is authoritative whenever it is present.
func (e Envelope) Succeeded() bool {
	if e.hasIsSucc {
		return e.IsSucc
	}
	return e.Code == 0
}

type ErrorKind string

const (
	ErrorKindInvalid         ErrorKind = "INVALID_ARGUMENT"
	ErrorKindUnauthenticated ErrorKind = "UNAUTHENTICATED"
	ErrorKindForbidden       ErrorKind = "FORBIDDEN"
	ErrorKindRateLimited     ErrorKind = "RATE_LIMITED"
	ErrorKindUpstream        ErrorKind = "UPSTREAM_ERROR"
	ErrorKindTimeout         ErrorKind = "UPSTREAM_TIMEOUT"
)

type Error struct {
	Kind       ErrorKind
	HTTPStatus int
	Code       int
	Message    string
	RequestID  string
	Cause      error
}

func (e *Error) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("admin-api: %s (upstream request_id=%s)", e.Message, e.RequestID)
	}
	return "admin-api: " + e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

// Retryable reports whether repeating the request could plausibly succeed.
// Timeouts, rate limits and failures of the exchange itself (Cause is set only
// for transport and body-read errors, where HTTPStatus may be absent) are
// transient. An upstream 4xx is a verdict on the request and will fail again.
func (e *Error) Retryable() bool {
	switch e.Kind {
	case ErrorKindTimeout, ErrorKindRateLimited:
		return true
	case ErrorKindUpstream:
		return e.Cause != nil || e.HTTPStatus == 0 || e.HTTPStatus >= http.StatusInternalServerError
	default:
		return false
	}
}

type Client struct {
	baseURL        *url.URL
	httpClient     *http.Client
	defaultTimeout time.Duration
}

// get performs a typed GET against an admin-api JSON envelope endpoint.
func get[T any](ctx context.Context, c *Client, endpoint string, query url.Values) (T, error) {
	var result T

	ctx, cancel := context.WithTimeout(ctx, c.defaultTimeout)
	defer cancel()

	request, err := c.newRequest(ctx, http.MethodGet, endpoint, query)
	if err != nil {
		return result, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return result, errorFromTransport(err)
	}
	defer response.Body.Close()

	body, err := readLimited(response.Body)
	if err != nil {
		return result, &Error{
			Kind:       ErrorKindUpstream,
			HTTPStatus: response.StatusCode,
			Message:    "read response failed",
			Cause:      err,
		}
	}
	if response.StatusCode != http.StatusOK {
		envelope, hasEnvelope := decodeEnvelope(body)
		return result, errorFromStatus(response.StatusCode, envelope, hasEnvelope)
	}

	envelope, rawData, hasEnvelope := decodeEnvelopeWithRaw(body)
	if !hasEnvelope {
		return result, &Error{
			Kind:       ErrorKindUpstream,
			HTTPStatus: response.StatusCode,
			Message:    "response is not an admin-api envelope",
		}
	}
	if !envelope.Succeeded() {
		return result, errorFromStatus(response.StatusCode, envelope, true)
	}
	if len(rawData) == 0 || string(bytes.TrimSpace(rawData)) == "null" {
		return result, nil
	}
	if err := json.Unmarshal(rawData, &result); err != nil {
		return result, &Error{
			Kind:       ErrorKindUpstream,
			HTTPStatus: response.StatusCode,
			Message:    "decode admin-api response data failed",
			Cause:      err,
		}
	}
	return result, nil
}

func NewClient(baseURL string, timeout time.Duration, credentials CredentialProvider, baseTransport http.RoundTripper) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse admin-api base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("admin-api base URL scheme must be http or https")
	}
	if parsed.Host == "" || parsed.Fragment != "" {
		return nil, errors.New("admin-api base URL must have a host and no fragment")
	}
	if timeout <= 0 || timeout > 30*time.Second {
		return nil, errors.New("admin-api timeout must be between 1ns and 30s")
	}

	return &Client{
		baseURL:        parsed,
		httpClient:     &http.Client{Transport: NewCredentialTransport(credentials, baseTransport)},
		defaultTimeout: timeout,
	}, nil
}

type HealthResult struct {
	Status     string    `json:"status"`
	HTTPStatus int       `json:"http_status"`
	RequestID  string    `json:"request_id"`
	CheckedAt  time.Time `json:"checked_at"`
}

func (c *Client) Health(ctx context.Context) (HealthResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.defaultTimeout)
	defer cancel()

	// admin-api does not currently expose /up. The permissions endpoint is a
	// temporary connectivity probe; any response proves the service is reachable,
	// even when it reports an HTTP or business error.
	request, err := c.newRequest(ctx, http.MethodGet, "/api/managers/permissions", nil)
	if err != nil {
		return HealthResult{}, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return HealthResult{}, errorFromTransport(err)
	}
	defer response.Body.Close()

	return HealthResult{
		Status:     "ok",
		HTTPStatus: response.StatusCode,
		RequestID:  response.Header.Get("X-Request-Id"),
		CheckedAt:  time.Now().UTC(),
	}, nil
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, query url.Values) (*http.Request, error) {
	if !strings.HasPrefix(endpoint, "/") {
		return nil, fmt.Errorf("%w: endpoint must start with /", errors.New("invalid endpoint"))
	}

	target := *c.baseURL
	target.Path = path.Join(target.Path, endpoint)
	if query != nil {
		target.RawQuery = query.Encode()
	}

	request, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create admin-api request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "internal-mcp-server/0.1")
	if identity, ok := auth.FromContext(ctx); ok {
		if identity.UserID != "" {
			request.Header.Set("X-Copilot-User-Id", identity.UserID)
		}
		if identity.SessionID != "" {
			request.Header.Set("X-Copilot-Session-Id", identity.SessionID)
		}
		if identity.RequestID != "" {
			request.Header.Set("X-Copilot-Request-Id", identity.RequestID)
		}
	}
	return request, nil
}

func readLimited(body io.Reader) ([]byte, error) {
	reader := io.LimitReader(body, maxResponseBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("response exceeds 1 MiB limit")
	}
	return data, nil
}

// envelopeFields mirrors Envelope with pointers so a missing key is
// distinguishable from a zero value.
type envelopeFields struct {
	IsSucc    *bool           `json:"is_succ"`
	Code      *int            `json:"code"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	RequestID string          `json:"request_id"`
}

// decodeEnvelope reports whether body is an admin-api response envelope, which
// requires is_succ or code to be present. Any other JSON body -- an empty
// object, a bare literal, or an unrelated payload such as {"status":"ok"} --
// is not an envelope and must not be read as a failure.
func decodeEnvelope(body []byte) (Envelope, bool) {
	envelope, rawData, ok := decodeEnvelopeWithRaw(body)
	if !ok {
		return Envelope{}, false
	}
	if len(rawData) > 0 {
		if err := json.Unmarshal(rawData, &envelope.Data); err != nil {
			return Envelope{}, false
		}
	}
	return envelope, true
}

func decodeEnvelopeWithRaw(body []byte) (Envelope, json.RawMessage, bool) {
	var fields envelopeFields
	if err := json.Unmarshal(body, &fields); err != nil {
		return Envelope{}, nil, false
	}
	if fields.IsSucc == nil && fields.Code == nil {
		return Envelope{}, nil, false
	}

	envelope := Envelope{
		Message:   fields.Message,
		Data:      fields.Data,
		RequestID: fields.RequestID,
	}
	if fields.IsSucc != nil {
		envelope.IsSucc = *fields.IsSucc
		envelope.hasIsSucc = true
	}
	if fields.Code != nil {
		envelope.Code = *fields.Code
	}
	return envelope, fields.Data, true
}

func errorFromTransport(err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: ErrorKindTimeout, Message: "admin-api request timed out", Cause: err}
	}
	return &Error{Kind: ErrorKindUpstream, Message: "admin-api request failed", Cause: err}
}

// errorFromStatus builds the error for a rejected exchange. status is the HTTP
// status, which is 200 when the rejection came from the envelope rather than
// the transport -- so the default message falls back to the status text only
// when that text actually describes a failure.
func errorFromStatus(status int, envelope Envelope, hasEnvelope bool) *Error {
	message := http.StatusText(status)
	if status < http.StatusBadRequest {
		message = "admin-api reported a failure"
	}
	code := status
	requestID := ""
	if hasEnvelope {
		if envelope.Message != "" {
			message = envelope.Message
		}
		code = envelope.Code
		requestID = envelope.RequestID
	}

	kind := ErrorKindUpstream
	switch {
	case status == http.StatusUnauthorized || (hasEnvelope && envelope.Code == 10000):
		kind = ErrorKindUnauthenticated
	case status == http.StatusForbidden:
		kind = ErrorKindForbidden
	case status == http.StatusTooManyRequests:
		kind = ErrorKindRateLimited
	case status == http.StatusUnprocessableEntity || (hasEnvelope && envelope.Code == 422):
		kind = ErrorKindInvalid
	}
	return &Error{Kind: kind, HTTPStatus: status, Code: code, Message: message, RequestID: requestID}
}
