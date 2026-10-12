package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadUsesDefaultsAndEnvOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	configJSON := `{
		"auth": {"service_token": "file-token"},
		"admin_api": {"timeout": 1000000000},
		"admin_console": {"base_url": "https://console.example.test"}
	}`
	if err := os.WriteFile(path, []byte(configJSON), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_SERVICE_TOKEN", "env-token")
	t.Setenv("ADMIN_API_BASE_URL", "https://admin.example.test")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != "127.0.0.1:8081" {
		t.Errorf("addr = %q", cfg.Server.Addr)
	}
	if cfg.Auth.ServiceToken != "env-token" {
		t.Errorf("service token = %q", cfg.Auth.ServiceToken)
	}
	if cfg.AdminAPI.BaseURL != "https://admin.example.test" {
		t.Errorf("base URL = %q", cfg.AdminAPI.BaseURL)
	}
	if cfg.AdminAPI.Timeout.Duration() != time.Second {
		t.Errorf("timeout = %s", cfg.AdminAPI.Timeout.Duration())
	}
	if len(cfg.Auth.DefaultPermissions) != 1 || cfg.Auth.DefaultPermissions[0] != defaultPermission {
		t.Errorf("default permissions = %v", cfg.Auth.DefaultPermissions)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"unknown":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_SERVICE_TOKEN", "token")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
}

func TestLoadRejectsRemovedBearerToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	configJSON := `{
		"auth": {"service_token": "token"},
		"admin_api": {
			"credentials": {"bearer_token": "removed"}
		}
	}`
	if err := os.WriteFile(path, []byte(configJSON), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), `unknown field "bearer_token"`) {
		t.Fatalf("bearer token error = %v", err)
	}
}

func TestValidateRejectsMissingTokenAndBadBaseURL(t *testing.T) {
	cfg := Config{}
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Auth.ServiceToken = "token"
	cfg.Auth.DefaultPermissions = []string{defaultPermission}
	cfg.AdminAPI.BaseURL = "ftp://admin"
	cfg.AdminAPI.Timeout = Duration(time.Second)
	cfg.AdminConsole.BaseURL = "https://console.example.test"
	cfg.RateLimit.RequestsPerMinute = 60
	cfg.RateLimit.Burst = 10
	cfg.LogLevel = "info"

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "scheme must be http or https") {
		t.Fatalf("validate error = %v", err)
	}
}

// admin_console.base_url has no default: navigation tools hand the resulting
// link to an operator, so a wrong host is worse than a failed startup.
func TestValidateRequiresAdminConsoleBaseURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"auth": {"service_token": "token"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "admin_console.base_url is required") {
		t.Fatalf("error = %v, want a missing admin_console.base_url failure", err)
	}
}

func TestValidateRejectsBadAdminConsoleBaseURL(t *testing.T) {
	cases := map[string]struct {
		baseURL string
		want    string
	}{
		"wrong scheme":    {baseURL: "ftp://console", want: "scheme must be http or https"},
		"no host":         {baseURL: "https://", want: "host is required"},
		"fragment":        {baseURL: "https://console.test/#/x", want: "must not contain a fragment"},
		"query string":    {baseURL: "https://console.test/?tenant=1", want: "must not contain a query string"},
		"only whitespace": {baseURL: "   ", want: "is required"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := Config{}
			cfg.Server.Addr = "127.0.0.1:0"
			cfg.Auth.ServiceToken = "token"
			cfg.Auth.DefaultPermissions = []string{defaultPermission}
			cfg.AdminAPI.BaseURL = "https://admin.example.test"
			cfg.AdminAPI.Timeout = Duration(time.Second)
			cfg.AdminConsole.BaseURL = test.baseURL
			cfg.RateLimit.RequestsPerMinute = 60
			cfg.RateLimit.Burst = 10
			cfg.LogLevel = "info"

			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadAppliesAdminConsoleEnvOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	configJSON := `{
		"auth": {"service_token": "token"},
		"admin_console": {"base_url": "https://from-file.test"}
	}`
	if err := os.WriteFile(path, []byte(configJSON), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADMIN_CONSOLE_BASE_URL", "https://from-env.test/console")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminConsole.BaseURL != "https://from-env.test/console" {
		t.Errorf("admin console base URL = %q", cfg.AdminConsole.BaseURL)
	}
}

func TestLoadRejectsMalformedEnvOverrides(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "timeout without unit", key: "ADMIN_API_TIMEOUT", value: "5000"},
		{name: "rate limit typo", key: "RATE_LIMIT_PER_MINUTE", value: "1O0"},
		{name: "burst typo", key: "RATE_LIMIT_BURST", value: "ten"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("MCP_SERVICE_TOKEN", "token")
			t.Setenv(test.key, test.value)

			_, err := Load("")
			if err == nil {
				t.Fatalf("%s=%s was accepted", test.key, test.value)
			}
			if !strings.Contains(err.Error(), test.key) {
				t.Errorf("error = %v, want it to name %s", err, test.key)
			}
		})
	}
}

func TestLoadParsesCookiesFromFileAndEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	configJSON := `{
		"auth": {"service_token": "token"},
		"admin_api": {
			"credentials": {"cookies": {"session_id": "from-file"}}
		},
		"admin_console": {"base_url": "https://console.example.test"}
	}`
	if err := os.WriteFile(path, []byte(configJSON), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminAPI.Credentials.Cookies["session_id"] != "from-file" {
		t.Errorf("cookies = %v", cfg.AdminAPI.Credentials.Cookies)
	}

	// The env override takes a whole Cookie header so it can be pasted
	// straight out of DevTools, and it replaces the file's set outright.
	t.Setenv("ADMIN_API_COOKIES", "session_id=from-env; XSRF-TOKEN=xsrf-env")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cookies := cfg.AdminAPI.Credentials.Cookies
	if len(cookies) != 2 || cookies["session_id"] != "from-env" || cookies["XSRF-TOKEN"] != "xsrf-env" {
		t.Errorf("cookies = %v", cookies)
	}
}

// A cookie value pasted without its name is the failure this schema exists to
// prevent: upstream cannot parse the header and answers only "Login required",
// so the config must be rejected at startup instead.
func TestLoadRejectsMalformedCookies(t *testing.T) {
	cases := map[string]string{
		"value without a name": `{"cookies": {"": "cookie-value-with-no-name"}}`,
		"empty value":          `{"cookies": {"session_id": ""}}`,
		"separator in value":   `{"cookies": {"session_id": "a; admin=1"}}`,
		"separator in name":    `{"cookies": {"session_id=x": "a"}}`,
	}
	for name, credentials := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			configJSON := `{"auth": {"service_token": "token"}, "admin_api": {"credentials": ` + credentials + `}}`
			if err := os.WriteFile(path, []byte(configJSON), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "cookies") {
				t.Fatalf("error = %v, want a cookies validation failure", err)
			}
		})
	}
}

func TestLoadRejectsCookieEnvWithoutName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"auth": {"service_token": "token"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADMIN_API_COOKIES", "cookie-value-with-no-name")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "name=value") {
		t.Fatalf("error = %v, want a name=value failure", err)
	}
}
