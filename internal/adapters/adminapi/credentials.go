package adminapi

import (
	"context"
	"net/http"
	"sort"
	"strings"
)

// Credentials carries the admin-api browser session. Every credential the
// upstream accepts is a cookie -- the session itself and the XSRF token alike
// -- so they are held as one name-indexed set rather than as a pre-rendered
// header plus separate token fields.
type Credentials struct {
	Cookies map[string]string
}

// CookieHeader renders the cookies as a Cookie request header value, sorted by
// name so the header is deterministic across calls. Entries missing a name or
// a value are dropped: a bare value carries no cookie name, and upstream
// discards the whole header when it cannot parse a name=value pair.
func (c Credentials) CookieHeader() string {
	names := make([]string, 0, len(c.Cookies))
	for name, value := range c.Cookies {
		if name != "" && value != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)

	pairs := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, name+"="+c.Cookies[name])
	}
	return strings.Join(pairs, "; ")
}

type CredentialProvider interface {
	Credentials(ctx context.Context) (Credentials, error)
}

type StaticCredentialProvider struct {
	credentials Credentials
}

func NewStaticCredentialProvider(credentials Credentials) *StaticCredentialProvider {
	return &StaticCredentialProvider{credentials: credentials}
}

func (p *StaticCredentialProvider) Credentials(context.Context) (Credentials, error) {
	return p.credentials, nil
}

type credentialTransport struct {
	base     http.RoundTripper
	provider CredentialProvider
}

func NewCredentialTransport(provider CredentialProvider, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &credentialTransport{base: base, provider: provider}
}

func (t *credentialTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	credentials, err := t.provider.Credentials(request.Context())
	if err != nil {
		return nil, err
	}

	out := request.Clone(request.Context())
	if header := credentials.CookieHeader(); header != "" {
		out.Header.Set("Cookie", header)
	}
	return t.base.RoundTrip(out)
}
