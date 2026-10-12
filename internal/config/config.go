package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

const defaultPermission = "admin.api.ping.read"

type Duration time.Duration

func (d *Duration) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" {
		return nil
	}
	if len(text) >= 2 && text[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return err
		}
		*d = Duration(parsed)
		return nil
	}

	raw, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fmt.Errorf("duration must be a number of nanoseconds or a duration string")
	}
	*d = Duration(raw)
	return nil
}

func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

type Config struct {
	Server       ServerConfig       `json:"server"`
	Auth         AuthConfig         `json:"auth"`
	AdminAPI     AdminAPIConfig     `json:"admin_api"`
	AdminConsole AdminConsoleConfig `json:"admin_console"`
	RateLimit    RateLimitConfig    `json:"rate_limit"`
	LogLevel     string             `json:"log_level"`
}

type ServerConfig struct {
	Addr string `json:"addr"`
}

type AuthConfig struct {
	ServiceToken       string   `json:"service_token"`
	DefaultPermissions []string `json:"default_permissions"`
}

type AdminAPIConfig struct {
	BaseURL     string            `json:"base_url"`
	Timeout     Duration          `json:"timeout"`
	Credentials CredentialsConfig `json:"credentials"`
}

type CredentialsConfig struct {
	// Cookies maps cookie name to value, e.g. {"session_id": "...",
	// "XSRF-TOKEN": "..."}. Both admin-api credentials are cookies, so neither
	// gets a field of its own here.
	Cookies map[string]string `json:"cookies"`
}

// AdminConsoleConfig points at the operator-facing admin console, not at its
// API. It exists only so navigation tools can turn a manifest route into an
// absolute link; nothing here is ever called over the network.
type AdminConsoleConfig struct {
	// BaseURL has no default on purpose. A plausible-but-wrong default would
	// produce dead links the model publishes confidently; failing startup is
	// the honest failure.
	BaseURL string `json:"base_url"`
}

type RateLimitConfig struct {
	RequestsPerMinute int `json:"requests_per_minute"`
	Burst             int `json:"burst"`
}

func Load(path string) (Config, error) {
	cfg := Config{
		Server: ServerConfig{Addr: "127.0.0.1:8081"},
		Auth: AuthConfig{
			DefaultPermissions: []string{defaultPermission},
		},
		AdminAPI: AdminAPIConfig{
			BaseURL: "http://127.0.0.1:8000",
			Timeout: Duration(5 * time.Second),
		},
		RateLimit: RateLimitConfig{
			RequestsPerMinute: 60,
			Burst:             10,
		},
		LogLevel: "info",
	}

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("decode config %s: %w", path, err)
		}
	}

	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applyEnv overlays environment overrides onto cfg. A malformed value is an
// error rather than a silent fallback to the previous setting: a typo in a
// limit must fail the deploy, not quietly run it at the default.
func applyEnv(cfg *Config) error {
	var errs []error
	if value := os.Getenv("SERVER_ADDR"); value != "" {
		cfg.Server.Addr = value
	}
	if value := os.Getenv("MCP_SERVICE_TOKEN"); value != "" {
		cfg.Auth.ServiceToken = value
	}
	if value := os.Getenv("MCP_DEFAULT_PERMISSIONS"); value != "" {
		cfg.Auth.DefaultPermissions = splitAndTrim(value)
	} else if cfg.Auth.DefaultPermissions == nil {
		cfg.Auth.DefaultPermissions = []string{defaultPermission}
	}
	if value := os.Getenv("ADMIN_API_BASE_URL"); value != "" {
		cfg.AdminAPI.BaseURL = value
	}
	if value := os.Getenv("ADMIN_API_TIMEOUT"); value != "" {
		duration, err := time.ParseDuration(value)
		if err != nil {
			errs = append(errs, fmt.Errorf("ADMIN_API_TIMEOUT %q: %w", value, err))
		} else {
			cfg.AdminAPI.Timeout = Duration(duration)
		}
	}
	if value := os.Getenv("ADMIN_API_COOKIES"); value != "" {
		cookies, err := parseCookieHeader(value)
		if err != nil {
			errs = append(errs, fmt.Errorf("ADMIN_API_COOKIES: %w", err))
		} else {
			cfg.AdminAPI.Credentials.Cookies = cookies
		}
	}
	if value := os.Getenv("ADMIN_CONSOLE_BASE_URL"); value != "" {
		cfg.AdminConsole.BaseURL = value
	}
	if value := os.Getenv("RATE_LIMIT_PER_MINUTE"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			errs = append(errs, fmt.Errorf("RATE_LIMIT_PER_MINUTE %q: %w", value, err))
		} else {
			cfg.RateLimit.RequestsPerMinute = parsed
		}
	}
	if value := os.Getenv("RATE_LIMIT_BURST"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			errs = append(errs, fmt.Errorf("RATE_LIMIT_BURST %q: %w", value, err))
		} else {
			cfg.RateLimit.Burst = parsed
		}
	}
	if value := os.Getenv("LOG_LEVEL"); value != "" {
		cfg.LogLevel = value
	}
	return errors.Join(errs...)
}

// parseCookieHeader parses a Cookie header value ("a=1; b=2") into a name to
// value map, so the env override can be pasted straight out of DevTools.
func parseCookieHeader(value string) (map[string]string, error) {
	cookies := make(map[string]string)
	for _, part := range strings.Split(value, ";") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		name, cookieValue, found := strings.Cut(trimmed, "=")
		if !found {
			return nil, fmt.Errorf("%q is not name=value", trimmed)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("%q has an empty cookie name", trimmed)
		}
		cookies[name] = strings.TrimSpace(cookieValue)
	}
	if len(cookies) == 0 {
		return nil, errors.New("no cookies found")
	}
	return cookies, nil
}

// validateCookies rejects anything that cannot be serialised into a Cookie
// header. A value carrying a separator or control character would truncate the
// header or splice in a second one, and an empty value silently drops the
// cookie -- both surface upstream only as an opaque "Login required".
func validateCookies(cookies map[string]string) []error {
	var errs []error
	for name, value := range cookies {
		switch {
		case strings.TrimSpace(name) == "":
			errs = append(errs, errors.New("admin_api.credentials.cookies has an empty cookie name"))
		case strings.ContainsAny(name, "=;, \t\r\n"):
			errs = append(errs, fmt.Errorf("admin_api.credentials.cookies name %q must not contain =, ; or whitespace", name))
		case value == "":
			errs = append(errs, fmt.Errorf("admin_api.credentials.cookies[%q] must not be empty", name))
		case strings.ContainsAny(value, ";\r\n"):
			errs = append(errs, fmt.Errorf("admin_api.credentials.cookies[%q] must not contain ; or a newline", name))
		}
	}
	return errs
}

func splitAndTrim(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// parseBaseURL applies the checks shared by every base URL in the config: an
// absolute http(s) URL with a host and no fragment. Callers layer their own
// rules on the returned URL.
func parseBaseURL(field, raw string) (*url.URL, []error) {
	parsed, err := url.Parse(raw)
	switch {
	case err != nil:
		return nil, []error{fmt.Errorf("%s: %w", field, err)}
	case parsed.Scheme != "http" && parsed.Scheme != "https":
		return nil, []error{fmt.Errorf("%s scheme must be http or https", field)}
	case parsed.Host == "":
		return nil, []error{fmt.Errorf("%s host is required", field)}
	case parsed.Fragment != "":
		return nil, []error{fmt.Errorf("%s must not contain a fragment", field)}
	}
	return parsed, nil
}

// validateAdminConsole rejects anything that cannot be safely prefixed onto a
// manifest route. There is no default: navigation tools hand the resulting link
// to an operator, and a link to the wrong host is worse than no link at all.
func validateAdminConsole(cfg AdminConsoleConfig) []error {
	raw := strings.TrimSpace(cfg.BaseURL)
	if raw == "" {
		return []error{errors.New("admin_console.base_url is required")}
	}
	parsed, errs := parseBaseURL("admin_console.base_url", raw)
	if len(errs) > 0 {
		return errs
	}
	// A query string on the base would be silently dropped once a route is
	// appended, so reject it rather than hand out a link that lost it.
	if parsed.RawQuery != "" {
		return []error{errors.New("admin_console.base_url must not contain a query string")}
	}
	return nil
}

func (c Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.Server.Addr) == "" {
		errs = append(errs, errors.New("server.addr is required"))
	}
	if c.Auth.ServiceToken == "" {
		errs = append(errs, errors.New("auth.service_token is required"))
	}
	if len(c.Auth.DefaultPermissions) == 0 {
		errs = append(errs, errors.New("auth.default_permissions must not be empty"))
	}
	_, urlErrs := parseBaseURL("admin_api.base_url", c.AdminAPI.BaseURL)
	errs = append(errs, urlErrs...)
	errs = append(errs, validateCookies(c.AdminAPI.Credentials.Cookies)...)
	errs = append(errs, validateAdminConsole(c.AdminConsole)...)
	timeout := c.AdminAPI.Timeout.Duration()
	if timeout <= 0 || timeout > 30*time.Second {
		errs = append(errs, errors.New("admin_api.timeout must be between 1ns and 30s"))
	}
	if c.RateLimit.RequestsPerMinute <= 0 || c.RateLimit.RequestsPerMinute > 10000 {
		errs = append(errs, errors.New("rate_limit.requests_per_minute must be between 1 and 10000"))
	}
	if c.RateLimit.Burst <= 0 || c.RateLimit.Burst > 1000 {
		errs = append(errs, errors.New("rate_limit.burst must be between 1 and 1000"))
	}
	if !slices.Contains([]string{"debug", "info", "warn", "error"}, c.LogLevel) {
		errs = append(errs, errors.New("log_level must be debug, info, warn, or error"))
	}
	return errors.Join(errs...)
}
