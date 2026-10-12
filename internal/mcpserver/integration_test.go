package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Global-MongoX/internal-mcp-server/internal/adapters/adminapi"
	"github.com/Global-MongoX/internal-mcp-server/internal/auth"
	"github.com/Global-MongoX/internal-mcp-server/internal/config"
	"github.com/Global-MongoX/internal-mcp-server/internal/httpapi"
	"github.com/Global-MongoX/internal-mcp-server/internal/mcpserver"
	"github.com/Global-MongoX/internal-mcp-server/internal/navigation"
	"github.com/Global-MongoX/internal-mcp-server/internal/registry"
	"github.com/Global-MongoX/internal-mcp-server/internal/toolruntime"
	navdata "github.com/Global-MongoX/internal-mcp-server/knowledge/navigation"
)

type identityTransport struct {
	base   http.RoundTripper
	scopes string
}

func (t *identityTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	out := request.Clone(request.Context())
	out.Header.Set("Authorization", "Bearer test-service-token")
	out.Header.Set("X-Copilot-User-Id", "user-1")
	out.Header.Set("X-Copilot-Session-Id", "session-1")
	out.Header.Set("X-Copilot-Request-Id", "request-1")
	if t.scopes != "" {
		out.Header.Set("X-Copilot-Scopes", t.scopes)
	}
	return t.base.RoundTrip(out)
}

// syncBuffer collects audit log lines written from server goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Split(strings.TrimSpace(b.buf.String()), "\n")
}

type fixture struct {
	adminAPI *httptest.Server
	server   *httptest.Server
	auditLog *syncBuffer
}

// testConsoleBaseURL is deliberately not the fake admin-api URL: navigation
// links point at the operator-facing console, which is a different host.
const (
	testConsoleBaseURL = "https://console.example.test"
	navigationScope    = "admin.console.navigation.read"
)

func newFixture(t *testing.T, upstream http.HandlerFunc, scopes string) *fixture {
	t.Helper()

	adminAPI := httptest.NewServer(upstream)
	t.Cleanup(adminAPI.Close)

	cfg := config.Config{
		Server: config.ServerConfig{Addr: "127.0.0.1:0"},
		Auth: config.AuthConfig{
			ServiceToken:       "test-service-token",
			DefaultPermissions: []string{"admin.api.ping.read", "admin.api.deposits.read"},
		},
		AdminAPI: config.AdminAPIConfig{
			BaseURL: adminAPI.URL,
			Timeout: config.Duration(time.Second),
		},
		AdminConsole: config.AdminConsoleConfig{BaseURL: testConsoleBaseURL},
		RateLimit:    config.RateLimitConfig{RequestsPerMinute: 100, Burst: 100},
		LogLevel:     "error",
	}

	auditLog := &syncBuffer{}
	auditLogger := slog.New(slog.NewJSONHandler(auditLog, &slog.HandlerOptions{Level: slog.LevelInfo}))

	adminClient, err := adminapi.NewClient(cfg.AdminAPI.BaseURL, cfg.AdminAPI.Timeout.Duration(), adminapi.NewStaticCredentialProvider(adminapi.Credentials{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	navigationManifest, err := navigation.Load(navdata.PagesYAML, cfg.AdminConsole.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	mcpHandler, err := mcpserver.Handler(mcpserver.Deps{
		Logger:     auditLogger,
		Registry:   registry.New(),
		Authorizer: auth.DefaultAuthorizer{},
		AdminAPI:   adminClient,
		Navigation: navigationManifest,
		Timeout:    cfg.AdminAPI.Timeout.Duration(),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(cfg, slog.New(slog.DiscardHandler), mcpHandler, adminClient).Handler())
	t.Cleanup(server.Close)

	return &fixture{adminAPI: adminAPI, server: server, auditLog: auditLog}
}

func connect(t *testing.T, serverURL, scopes string) *mcp.ClientSession {
	t.Helper()

	client := mcp.NewClient(&mcp.Implementation{Name: "contract-test", Version: "1.0.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   serverURL + "/mcp",
		HTTPClient: &http.Client{Transport: &identityTransport{base: http.DefaultTransport, scopes: scopes}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect MCP: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func decodeEnvelope(t *testing.T, result *mcp.CallToolResult) toolruntime.Envelope[map[string]any] {
	t.Helper()

	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var envelope toolruntime.Envelope[map[string]any]
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope %s: %v", raw, err)
	}
	return envelope
}

func TestToolContractAndSuccessfulCall(t *testing.T) {
	fixture := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", "upstream-request")
		_, _ = w.Write([]byte("OK"))
	}, "admin.api.ping.read")

	session := connect(t, fixture.server.URL, "admin.api.ping.read")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) != 8 {
		t.Fatalf("tools = %+v", tools.Tools)
	}
	var tool *mcp.Tool
	for _, candidate := range tools.Tools {
		if candidate.Name == "admin_api_ping" {
			tool = candidate
			break
		}
	}
	if tool == nil {
		t.Fatalf("ping tool missing from %+v", tools.Tools)
	}
	if tool.Description == "" || tool.Title != "Admin API Health Ping" {
		t.Errorf("tool metadata = %+v", tool)
	}

	inputSchema, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(inputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["type"] != "object" {
		t.Errorf("input schema = %s", inputSchema)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "admin_api_ping",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool result = %+v", result)
	}
	envelope := decodeEnvelope(t, result)
	if envelope.Status != toolruntime.StatusSuccess || envelope.Error != nil {
		t.Fatalf("envelope = %+v", envelope)
	}
	if envelope.Data == nil {
		t.Fatal("data is nil")
	}
	data := *envelope.Data
	if data["status"] != "ok" || data["upstream_http_status"] != float64(http.StatusOK) {
		t.Errorf("data = %v", data)
	}
	if envelope.Source == nil || envelope.Source.Service != "admin-api" || envelope.Source.Resource != "health" {
		t.Errorf("source = %+v", envelope.Source)
	}
}

func TestDepositToolsExposeBusinessContracts(t *testing.T) {
	var overviewQuery, statisticsQuery, depositsQuery string
	fixture := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/managers/permissions":
			_, _ = w.Write([]byte("OK"))
		case "/data_overview":
			overviewQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"is_succ":true,"code":0,"data":{"registered_users":13,"first_deposit_users":7,"trading_users":14,"trading_lots":131.83,"trading_amount":51842.38,"order_count":95,"deposit_amount":1234.50,"withdrawal_amount":234.25,"net_deposit_amount":1000.25,"ib_commission_amount":37.5564,"ib_commission_customer_count":3,"master_commission_amount":0.308062,"master_commission_customer_count":2}}`))
		case "/deposits/statistics":
			statisticsQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"is_succ":true,"code":0,"data":{"total_count":12,"customer_count":8,"chain_statistics":[{"chain_id":"eip155:11155111","chain_name":"Ethereum Sepolia Testnet(ERC20)","total_amount":1200.50,"total_count":8,"customer_count":5}]}}`))
		case "/deposits":
			depositsQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"is_succ":true,"code":0,"data":{"data":[{"id":9,"user_code":123,"order_no":"order-1","apply_amount":"88.80","network":"eip155:11155111","network_text":"Ethereum Sepolia Testnet(ERC20)"}],"total_count":31}}`))
		default:
			http.NotFound(w, r)
		}
	}, "admin.api.ping.read,admin.api.deposits.read,admin.api.commissions.read")

	session := connect(t, fixture.server.URL, "admin.api.ping.read,admin.api.deposits.read,admin.api.commissions.read")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	wantTools := map[string]bool{
		"admin_api_data_overview":       false,
		"admin_api_deposits_statistics": false,
		"admin_api_deposits_list":       false,
	}
	schemas := map[string]map[string]any{}
	outputSchemas := map[string]map[string]any{}
	for _, tool := range tools.Tools {
		if _, wanted := wantTools[tool.Name]; !wanted {
			continue
		}
		wantTools[tool.Name] = true
		if tool.OutputSchema == nil {
			t.Errorf("tool %q has no output schema", tool.Name)
		} else {
			rawOutputSchema, err := json.Marshal(tool.OutputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var outputSchema map[string]any
			if err := json.Unmarshal(rawOutputSchema, &outputSchema); err != nil {
				t.Fatal(err)
			}
			outputSchemas[tool.Name] = outputSchema
		}
		rawSchema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]any
		if err := json.Unmarshal(rawSchema, &schema); err != nil {
			t.Fatal(err)
		}
		schemas[tool.Name] = schema
	}
	for name, found := range wantTools {
		if !found {
			t.Errorf("tool %q was not listed", name)
		}
	}

	rawRequired, err := json.Marshal(schemas["admin_api_data_overview"]["required"])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"date_start", "date_end", "trade_account_type", "customer_type", "source_trading_type", "chart_date_type"} {
		if !strings.Contains(string(rawRequired), field) {
			t.Errorf("required schema %s missing %q", rawRequired, field)
		}
	}
	if schemas["admin_api_deposits_list"]["required"] != nil {
		t.Errorf("deposit list required = %v, want none", schemas["admin_api_deposits_list"]["required"])
	}
	rawStatisticsRequired, err := json.Marshal(schemas["admin_api_deposits_statistics"]["required"])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"start_time", "end_time"} {
		if !strings.Contains(string(rawStatisticsRequired), field) {
			t.Errorf("statistics required schema %s missing %q", rawStatisticsRequired, field)
		}
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "admin_api_data_overview",
		Arguments: map[string]any{
			"date_start":          "2026-09-01",
			"date_end":            "2026-09-07",
			"trade_account_type":  "real",
			"customer_type":       "all",
			"source_trading_type": "lot",
			"chart_date_type":     "week",
		},
	})
	if err != nil {
		t.Fatalf("call total amount: %v", err)
	}
	envelope := decodeEnvelope(t, result)
	data := *envelope.Data
	if envelope.Status != toolruntime.StatusSuccess {
		t.Fatalf("overview envelope = %+v", envelope)
	}
	// Every field admin-api returns must reach the caller: the tool used to drop
	// registration, trading and withdrawal figures on the floor.
	customers, ok := data["customers"].(map[string]any)
	if !ok || customers["registered_count"] != float64(13) || customers["trading_count"] != float64(14) {
		t.Fatalf("overview customers = %+v", data["customers"])
	}
	trading, ok := data["trading"].(map[string]any)
	if !ok || trading["lots"] != 131.83 || trading["amount"] != 51842.38 || trading["order_count"] != float64(95) || trading["currency"] != "USDT" {
		t.Fatalf("overview trading = %+v", data["trading"])
	}
	deposits, ok := data["deposits"].(map[string]any)
	if !ok || deposits["total_amount"] != 1234.5 || deposits["first_deposit_customer_count"] != float64(7) ||
		deposits["net_amount"] != 1000.25 || deposits["withdrawal_amount"] != 234.25 || deposits["currency"] != "USDT" {
		t.Fatalf("overview deposits = %+v", data["deposits"])
	}
	agentCommission, ok := data["agent_commission"].(map[string]any)
	if !ok || agentCommission["total_amount"] != 37.5564 || agentCommission["customer_count"] != float64(3) || agentCommission["currency"] != "USDT" {
		t.Fatalf("overview agent commission = %+v", data["agent_commission"])
	}
	masterProfitSharing, ok := data["master_profit_sharing"].(map[string]any)
	if !ok || masterProfitSharing["total_amount"] != 0.308062 || masterProfitSharing["customer_count"] != float64(2) ||
		masterProfitSharing["currency"] != "USDT" {
		t.Fatalf("overview master profit sharing = %+v", data["master_profit_sharing"])
	}
	if envelope.Source == nil || envelope.Source.Resource != "data_overview" {
		t.Errorf("total amount source = %+v", envelope.Source)
	}
	wantOverviewQuery := "chart_date_type=week&customer_type=all&date_end=2026-09-07&date_start=2026-09-01&source_trading_type=lot&trade_account_type=real"
	if overviewQuery != wantOverviewQuery {
		t.Errorf("overview query = %q", overviewQuery)
	}
	requireNumberSchema(t, outputSchemas["admin_api_data_overview"], "data", "trading", "lots")
	requireNumberSchema(t, outputSchemas["admin_api_data_overview"], "data", "trading", "amount")
	requireNumberSchema(t, outputSchemas["admin_api_data_overview"], "data", "deposits", "withdrawal_amount")
	requireNumberSchema(t, outputSchemas["admin_api_data_overview"], "data", "deposits", "total_amount")
	requireNumberSchema(t, outputSchemas["admin_api_data_overview"], "data", "deposits", "net_amount")
	requireNumberSchema(t, outputSchemas["admin_api_data_overview"], "data", "agent_commission", "total_amount")
	requireNumberSchema(t, outputSchemas["admin_api_data_overview"], "data", "master_profit_sharing", "total_amount")

	result, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "admin_api_deposits_statistics",
		Arguments: map[string]any{
			"start_time": "2026-09-01",
			"end_time":   "2026-09-07",
		},
	})
	if err != nil {
		t.Fatalf("call deposit statistics: %v", err)
	}
	envelope = decodeEnvelope(t, result)
	if envelope.Status != toolruntime.StatusSuccess {
		t.Fatalf("statistics envelope = %+v", envelope)
	}
	if envelope.Source == nil || envelope.Source.Resource != "deposits/statistics" {
		t.Fatalf("statistics source = %+v", envelope.Source)
	}
	data = *envelope.Data
	if data["total_count"] != float64(12) || data["customer_count"] != float64(8) {
		t.Fatalf("statistics totals = %+v", data)
	}
	chains, ok := data["chains"].([]any)
	if !ok || len(chains) != 1 {
		t.Fatalf("statistics chains = %+v", data["chains"])
	}
	chain := chains[0].(map[string]any)
	if chain["chain_id"] != "eip155:11155111" || chain["total_amount"] != 1200.5 || chain["currency"] != "USDT" || chain["total_count"] != float64(8) || chain["customer_count"] != float64(5) {
		t.Fatalf("chain = %+v", chain)
	}
	if statisticsQuery != "end_time=2026-09-07&start_time=2026-09-01" {
		t.Errorf("statistics query = %q", statisticsQuery)
	}
	requireNumberSchema(t, outputSchemas["admin_api_deposits_statistics"], "data", "[]chains", "total_amount")

	result, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "admin_api_deposits_list",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call deposit list: %v", err)
	}
	envelope = decodeEnvelope(t, result)
	if envelope.Status != toolruntime.StatusSuccess || envelope.Pagination == nil {
		t.Fatalf("deposit list envelope = %+v", envelope)
	}
	if envelope.Source == nil || envelope.Source.Resource != "deposits" {
		t.Fatalf("deposit list source = %+v", envelope.Source)
	}
	if envelope.Pagination.Limit != 20 || envelope.Pagination.Offset != 0 || envelope.Pagination.Total != 31 {
		t.Errorf("pagination = %+v", envelope.Pagination)
	}
	items, ok := (*envelope.Data)["items"].([]any)
	if !ok || len(items) != 1 || items[0].(map[string]any)["network_text"] != "Ethereum Sepolia Testnet(ERC20)" {
		t.Fatalf("deposit list data = %+v", *envelope.Data)
	}
	if depositsQuery != "limit=20&offset=0" {
		t.Errorf("deposits query = %q", depositsQuery)
	}
	requireNumberSchema(t, outputSchemas["admin_api_deposits_list"], "data", "[]items", "apply_amount")
}

func TestCommissionToolsExposeBusinessContracts(t *testing.T) {
	var statisticsQuery, detailPath, detailQuery string
	fixture := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/managers/permissions":
			_, _ = w.Write([]byte("OK"))
		case r.URL.Path == "/data_overview":
			_, _ = w.Write([]byte(`{"is_succ":true,"code":0,"data":{"ib_commission_amount":37.5564,"master_commission_amount":2.079545}}`))
		case r.URL.Path == "/commission/statistics":
			statisticsQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"is_succ":true,"code":0,"data":{"ib_count":6,"master_count":3}}`))
		case r.URL.Path == "/ib/123/commissions":
			detailPath = r.URL.Path
			detailQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"is_succ":true,"code":0,"data":{"data":[{"commission_amount":"10.50","settled_at":"2026-09-02 11:00:00"}],"total_count":2}}`))
		default:
			http.NotFound(w, r)
		}
	}, "admin.api.ping.read,admin.api.commissions.read")

	session := connect(t, fixture.server.URL, "admin.api.ping.read,admin.api.commissions.read")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	wantCommissionTools := map[string]bool{
		"admin_api_commission_statistics":     false,
		"admin_api_commissions_agent_details": false,
	}
	commissionSchemas := map[string]map[string]any{}
	for _, tool := range tools.Tools {
		if _, wanted := wantCommissionTools[tool.Name]; !wanted {
			continue
		}
		wantCommissionTools[tool.Name] = true
		if tool.OutputSchema == nil {
			t.Errorf("tool %q has no output schema", tool.Name)
		}
		rawSchema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]any
		if err := json.Unmarshal(rawSchema, &schema); err != nil {
			t.Fatal(err)
		}
		commissionSchemas[tool.Name] = schema
	}
	for name, found := range wantCommissionTools {
		if !found {
			t.Errorf("tool %q was not listed", name)
		}
	}
	rawCommissionRequired, err := json.Marshal(commissionSchemas["admin_api_commission_statistics"]["required"])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"start_time", "end_time"} {
		if !strings.Contains(string(rawCommissionRequired), field) {
			t.Errorf("commission required schema %s missing %q", rawCommissionRequired, field)
		}
	}
	rawDetailRequired, err := json.Marshal(commissionSchemas["admin_api_commissions_agent_details"]["required"])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"user_code", "start_time", "end_time"} {
		if !strings.Contains(string(rawDetailRequired), field) {
			t.Errorf("detail required schema %s missing %q", rawDetailRequired, field)
		}
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "admin_api_commission_statistics",
		Arguments: map[string]any{
			"start_time": "2026-09-01",
			"end_time":   "2026-09-07",
		},
	})
	if err != nil {
		t.Fatalf("call agent count: %v", err)
	}
	envelope := decodeEnvelope(t, result)
	data := *envelope.Data
	if envelope.Status != toolruntime.StatusSuccess || data["agent_count"] != float64(6) || data["master_count"] != float64(3) {
		t.Fatalf("commission statistics envelope = %+v", envelope)
	}
	if statisticsQuery != "end_time=2026-09-07&start_time=2026-09-01" {
		t.Errorf("statistics query = %q", statisticsQuery)
	}
	if envelope.Source == nil || envelope.Source.Resource != "commission/statistics" {
		t.Fatalf("commission statistics source = %+v", envelope.Source)
	}

	result, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "admin_api_commissions_agent_details",
		Arguments: map[string]any{
			"user_code":  123,
			"start_time": "2026-09-01",
			"end_time":   "2026-09-07",
		},
	})
	if err != nil {
		t.Fatalf("call agent details: %v", err)
	}
	envelope = decodeEnvelope(t, result)
	if envelope.Status != toolruntime.StatusSuccess || envelope.Pagination == nil || envelope.Pagination.Total != 2 {
		t.Fatalf("agent details envelope = %+v", envelope)
	}
	items, ok := (*envelope.Data)["items"].([]any)
	if !ok || len(items) != 1 || items[0].(map[string]any)["commission_amount"] != "10.50" {
		t.Fatalf("agent details data = %+v", *envelope.Data)
	}
	if detailPath != "/ib/123/commissions" || detailQuery != "end_time=2026-09-07&limit=20&offset=0&start_time=2026-09-01" {
		t.Errorf("detail request = %s?%s", detailPath, detailQuery)
	}
	if envelope.Source == nil || envelope.Source.Resource != "ib/commissions" {
		t.Fatalf("agent details source = %+v", envelope.Source)
	}
}

func TestToolAuthorizationRejectsMissingScope(t *testing.T) {
	fixture := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("OK"))
	}, "")

	session := connect(t, fixture.server.URL, "some.other.permission")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "admin_api_ping",
		Arguments: map[string]any{},
	})
	if err == nil {
		t.Fatal("unauthorized tool call succeeded")
	}

	// A rejected call is exactly what the audit log exists for, so it must be
	// recorded even though authorization never reached the tool.
	if !hasAuditedToolCall(t, fixture, "admin_api_ping") {
		t.Errorf("rejected tool call was not audited: %v", fixture.auditLog.lines())
	}
}

func TestDataOverviewRequiresAllDomainScopes(t *testing.T) {
	upstream := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/data_overview" {
			_, _ = w.Write([]byte(`{"is_succ":true,"code":0,"data":{"deposit_amount":1}}`))
			return
		}
		http.NotFound(w, r)
	}
	tests := []struct {
		name   string
		scopes string
	}{
		{name: "deposit scope only", scopes: "admin.api.deposits.read"},
		{name: "commission scope only", scopes: "admin.api.commissions.read"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t, upstream, test.scopes)
			session := connect(t, fixture.server.URL, test.scopes)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			_, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: "admin_api_data_overview",
				Arguments: map[string]any{
					"date_start":          "2026-09-01",
					"date_end":            "2026-09-07",
					"trade_account_type":  "real",
					"customer_type":       "all",
					"source_trading_type": "lot",
					"chart_date_type":     "week",
				},
			})
			if err == nil {
				t.Fatal("data overview was allowed without both domain scopes")
			}
		})
	}
}

func requireNumberSchema(t *testing.T, outputSchema map[string]any, path ...string) {
	t.Helper()
	if outputSchema == nil {
		t.Fatal("output schema missing")
	}
	current := outputSchema
	for _, segment := range path {
		if arrayName, ok := strings.CutPrefix(segment, "[]"); ok {
			properties, ok := current["properties"].(map[string]any)
			if !ok {
				t.Fatalf("schema path %v has no properties before array %q: %+v", path, arrayName, current)
			}
			arraySchema, ok := properties[arrayName].(map[string]any)
			if !ok {
				t.Fatalf("schema path %v missing array %q: %+v", path, arrayName, properties)
			}
			next, ok := arraySchema["items"].(map[string]any)
			if !ok {
				t.Fatalf("schema path %v array %q has no items: %+v", path, arrayName, arraySchema)
			}
			current = next
			continue
		}
		properties, ok := current["properties"].(map[string]any)
		if !ok {
			t.Fatalf("schema path %v has no properties at %q: %+v", path, segment, current)
		}
		next, ok := properties[segment].(map[string]any)
		if !ok {
			t.Fatalf("schema path %v missing %q: %+v", path, segment, properties)
		}
		current = next
	}
	if current["type"] != "number" {
		t.Fatalf("schema path %v type = %+v, want number", path, current["type"])
	}
}

func hasAuditedToolCall(t *testing.T, fixture *fixture, tool string) bool {
	t.Helper()

	for _, line := range fixture.auditLog.lines() {
		if line == "" {
			continue
		}
		var entry struct {
			Msg   string `json:"msg"`
			Tool  string `json:"tool"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode audit line %s: %v", line, err)
		}
		if entry.Msg == "mcp request audited" && entry.Tool == tool && entry.Error != "" {
			return true
		}
	}
	return false
}

func TestUpstreamFailureReturnsStructuredToolError(t *testing.T) {
	fixture := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/managers/permissions" {
			_, _ = w.Write([]byte("OK"))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("database unavailable"))
	}, "admin.api.deposits.read,admin.api.commissions.read")

	session := connect(t, fixture.server.URL, "admin.api.deposits.read,admin.api.commissions.read")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "admin_api_data_overview",
		Arguments: map[string]any{
			"date_start":          "2026-09-01",
			"date_end":            "2026-09-07",
			"trade_account_type":  "real",
			"customer_type":       "all",
			"source_trading_type": "lot",
			"chart_date_type":     "week",
		},
	})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if result.IsError {
		t.Fatal("downstream failure should remain a structured tool result")
	}
	envelope := decodeEnvelope(t, result)
	if envelope.Status != toolruntime.StatusError || envelope.Error == nil {
		t.Fatalf("envelope = %+v", envelope)
	}
	if envelope.Error.Code != "UPSTREAM_ERROR" || !envelope.Error.Retryable {
		t.Errorf("error = %+v", envelope.Error)
	}
}

func TestPermanentUpstreamFailureIsNotRetryable(t *testing.T) {
	fixture := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/managers/permissions" {
			_, _ = w.Write([]byte("OK"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no such endpoint"))
	}, "admin.api.deposits.read,admin.api.commissions.read")

	session := connect(t, fixture.server.URL, "admin.api.deposits.read,admin.api.commissions.read")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "admin_api_data_overview",
		Arguments: map[string]any{
			"date_start":          "2026-09-01",
			"date_end":            "2026-09-07",
			"trade_account_type":  "real",
			"customer_type":       "all",
			"source_trading_type": "lot",
			"chart_date_type":     "week",
		},
	})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	envelope := decodeEnvelope(t, result)
	if envelope.Error == nil {
		t.Fatalf("envelope = %+v", envelope)
	}
	// A 404 is a verdict on the request; retrying it can only fail again.
	if envelope.Error.Retryable {
		t.Errorf("error = %+v, want retryable=false", envelope.Error)
	}
}

func navigationTool(t *testing.T, tools *mcp.ListToolsResult, name string) *mcp.Tool {
	t.Helper()
	for _, candidate := range tools.Tools {
		if candidate.Name == name {
			return candidate
		}
	}
	t.Fatalf("tool %q missing from %+v", name, tools.Tools)
	return nil
}

func TestNavigationToolsExposeContracts(t *testing.T) {
	fixture := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("OK"))
	}, navigationScope)

	session := connect(t, fixture.server.URL, navigationScope)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	findPage := navigationTool(t, tools, "admin_console_find_page")
	// The description is the Agent's only input for deciding when to reach for
	// this tool, so the menu groups it advertises come from the manifest and
	// cannot drift from it.
	for _, group := range []string{"资金管理", "客户管理", "设置"} {
		if !strings.Contains(findPage.Description, group) {
			t.Errorf("find_page description omits menu group %q", group)
		}
	}
	navigationTool(t, tools, "admin_console_get_page")

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "admin_console_find_page",
		Arguments: map[string]any{"query": "入金记录在后台哪里看"},
	})
	if err != nil {
		t.Fatalf("call find_page: %v", err)
	}
	envelope := decodeEnvelope(t, result)
	if envelope.Status != toolruntime.StatusSuccess {
		t.Fatalf("envelope = %+v", envelope)
	}
	// Navigation data is local, so it must not claim admin-api as its source.
	if envelope.Source == nil || envelope.Source.Service != "internal-mcp-server" ||
		envelope.Source.Resource != "knowledge/navigation/pages" {
		t.Errorf("source = %+v", envelope.Source)
	}

	data := *envelope.Data
	matches, _ := data["matches"].([]any)
	if len(matches) == 0 {
		t.Fatalf("matches = %v", data["matches"])
	}
	first, _ := matches[0].(map[string]any)
	if first["id"] != "finance.cash.deposit" {
		t.Fatalf("first match = %v", first)
	}
	if first["url"] != testConsoleBaseURL+"/finance/deposit" {
		t.Errorf("url = %v, want it rooted at the configured console base URL", first["url"])
	}
	// menu_key ships under access with checked false: it is the console's own
	// menu code, not an MCP permission, and nothing here evaluated it.
	access, _ := first["access"].(map[string]any)
	if access["menu_key"] != "finance.cash.deposit" || access["checked"] != false {
		t.Errorf("access = %v", access)
	}
	if _, leaked := first["api_endpoints"]; leaked {
		t.Error("api_endpoints leaked into the tool result")
	}
	if _, leaked := first["reviewed"]; leaked {
		t.Error("reviewed leaked into the tool result")
	}

	links, _ := data["navigation"].([]any)
	if len(links) == 0 {
		t.Fatalf("navigation = %v", data["navigation"])
	}
	link, _ := links[0].(map[string]any)
	if link["url"] != testConsoleBaseURL+"/finance/deposit" {
		t.Errorf("navigation link = %v", link)
	}
}

// A documented absence is a real answer, not a failure: SUCCESS with no matches
// and the gap that explains why.
func TestFindPageReportsKnownGapsAsSuccess(t *testing.T) {
	fixture := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("OK"))
	}, navigationScope)
	session := connect(t, fixture.server.URL, navigationScope)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "admin_console_find_page",
		Arguments: map[string]any{"query": "后台的 KYC 审核在哪"},
	})
	if err != nil {
		t.Fatalf("call find_page: %v", err)
	}
	if result.IsError {
		t.Fatalf("result = %+v", result)
	}
	envelope := decodeEnvelope(t, result)
	if envelope.Status != toolruntime.StatusSuccess {
		t.Fatalf("envelope = %+v", envelope)
	}
	// An informative empty result is not a partial failure, so it must not
	// raise a warning the Agent would surface to the operator.
	if len(envelope.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", envelope.Warnings)
	}
	data := *envelope.Data
	if matches, _ := data["matches"].([]any); len(matches) != 0 {
		t.Errorf("matches = %v, want none", matches)
	}
	gaps, _ := data["known_gaps"].([]any)
	if len(gaps) == 0 {
		t.Fatalf("known_gaps = %v, want the KYC gap", data["known_gaps"])
	}
	gap, _ := gaps[0].(map[string]any)
	if !strings.Contains(gap["topic"].(string), "KYC") {
		t.Errorf("gap = %v", gap)
	}
	if groups, _ := data["menu_groups"].([]any); len(groups) != 9 {
		t.Errorf("menu_groups = %v, want all nine", data["menu_groups"])
	}
}

func TestGetPageResolvesEntityParams(t *testing.T) {
	fixture := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("OK"))
	}, navigationScope)
	session := connect(t, fixture.server.URL, navigationScope)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "admin_console_get_page",
		Arguments: map[string]any{
			"page_id":       "settings.roles.settings:edit",
			"entity_params": map[string]any{"roleId": "12"},
		},
	})
	if err != nil {
		t.Fatalf("call get_page: %v", err)
	}
	envelope := decodeEnvelope(t, result)
	if envelope.Status != toolruntime.StatusSuccess {
		t.Fatalf("envelope = %+v", envelope)
	}
	page, _ := (*envelope.Data)["page"].(map[string]any)
	if page["url"] != testConsoleBaseURL+"/roles/edit/12" {
		t.Fatalf("url = %v", page["url"])
	}
}

// Bad input comes back as a structured envelope, never a transport error --
// the same contract TestUpstreamFailureReturnsStructuredToolError pins.
func TestNavigationBadInputReturnsStructuredError(t *testing.T) {
	fixture := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("OK"))
	}, navigationScope)
	session := connect(t, fixture.server.URL, navigationScope)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	tests := map[string]*mcp.CallToolParams{
		"unknown page": {
			Name:      "admin_console_get_page",
			Arguments: map[string]any{"page_id": "finance.cash.nope"},
		},
		"unsafe entity value": {
			Name: "admin_console_get_page",
			Arguments: map[string]any{
				"page_id":       "settings.roles.settings:edit",
				"entity_params": map[string]any{"roleId": "../../admin"},
			},
		},
		"empty query": {
			Name:      "admin_console_find_page",
			Arguments: map[string]any{"query": "   "},
		},
		"unknown menu group": {
			Name:      "admin_console_find_page",
			Arguments: map[string]any{"query": "入金", "menu_group": "不存在的菜单"},
		},
	}
	for name, params := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(ctx, params)
			if err != nil {
				t.Fatalf("call tool: %v", err)
			}
			if result.IsError {
				t.Fatalf("result = %+v, want a structured envelope", result)
			}
			envelope := decodeEnvelope(t, result)
			if envelope.Status != toolruntime.StatusError || envelope.Error == nil {
				t.Fatalf("envelope = %+v", envelope)
			}
			if envelope.Error.Code != "INVALID_ARGUMENT" || envelope.Error.Retryable {
				t.Errorf("error = %+v", envelope.Error)
			}
		})
	}
}

func TestNavigationToolsRequireTheirOwnScope(t *testing.T) {
	fixture := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("OK"))
	}, "admin.api.deposits.read")
	session := connect(t, fixture.server.URL, "admin.api.deposits.read")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if _, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "admin_console_find_page",
		Arguments: map[string]any{"query": "入金"},
	}); err == nil {
		t.Fatal("find_page was callable without admin.console.navigation.read")
	}
}
