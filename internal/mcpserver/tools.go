package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	jsonschema "github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Global-MongoX/internal-mcp-server/internal/adapters/adminapi"
	"github.com/Global-MongoX/internal-mcp-server/internal/registry"
	"github.com/Global-MongoX/internal-mcp-server/internal/toolruntime"
)

const (
	depositsPermission    = "admin.api.deposits.read"
	commissionsPermission = "admin.api.commissions.read"
)

// usdtCurrency annotates every monetary field admin-api returns, so callers
// never have to infer the unit from a tool description.
const usdtCurrency = "USDT"

type DataOverviewInput struct {
	DateStart         string `json:"date_start" jsonschema:"Start date in YYYY-MM-DD format"`
	DateEnd           string `json:"date_end" jsonschema:"End date in YYYY-MM-DD format"`
	TradeAccountType  string `json:"trade_account_type" jsonschema:"Account type: real or trial"`
	CustomerType      string `json:"customer_type" jsonschema:"Customer type: all or ib"`
	SourceTradingType string `json:"source_trading_type" jsonschema:"Source ranking dimension: lot, amount, or order_count"`
	ChartDateType     string `json:"chart_date_type" jsonschema:"Chart granularity: day, week, or month"`
}

type DataOverviewData struct {
	Customers           CustomerOverview          `json:"customers"`
	Trading             TradingOverview           `json:"trading"`
	Deposits            DepositOverview           `json:"deposits"`
	AgentCommission     CommissionAmount          `json:"agent_commission"`
	MasterProfitSharing MasterProfitSharingAmount `json:"master_profit_sharing"`
}

type CustomerOverview struct {
	RegisteredCount int64 `json:"registered_count" jsonschema:"Customers who registered in the period"`
	TradingCount    int64 `json:"trading_count" jsonschema:"Customers who placed at least one order in the period"`
}

type TradingOverview struct {
	Lots       json.Number `json:"lots"`
	Amount     json.Number `json:"amount"`
	OrderCount int64       `json:"order_count"`
	Currency   string      `json:"currency" jsonschema:"Currency of amount"`
}

type DepositOverview struct {
	TotalAmount               json.Number `json:"total_amount"`
	FirstDepositCustomerCount int64       `json:"first_deposit_customer_count"`
	NetAmount                 json.Number `json:"net_amount" jsonschema:"total_amount minus withdrawal_amount"`
	WithdrawalAmount          json.Number `json:"withdrawal_amount"`
	Currency                  string      `json:"currency" jsonschema:"Currency of total_amount, net_amount and withdrawal_amount"`
}

type CommissionAmount struct {
	TotalAmount   json.Number `json:"total_amount"`
	CustomerCount int64       `json:"customer_count" jsonschema:"Agents paid commission in the period"`
	Currency      string      `json:"currency" jsonschema:"Currency of total_amount"`
}

type MasterProfitSharingAmount struct {
	TotalAmount   json.Number `json:"total_amount"`
	CustomerCount int64       `json:"customer_count" jsonschema:"Masters paid profit sharing in the period"`
	Currency      string      `json:"currency" jsonschema:"Currency of total_amount"`
}

type DateRangeInput struct {
	StartTime string `json:"start_time" jsonschema:"Start date or datetime"`
	EndTime   string `json:"end_time" jsonschema:"End date or datetime"`
}

type DepositStatisticsData struct {
	TotalCount    int64                    `json:"total_count"`
	CustomerCount int64                    `json:"customer_count"`
	Chains        []DepositChainStatistics `json:"chains"`
}

type DepositChainStatistics struct {
	ChainID       string      `json:"chain_id"`
	ChainName     string      `json:"chain_name"`
	TotalAmount   json.Number `json:"total_amount"`
	Currency      string      `json:"currency" jsonschema:"Currency of total_amount"`
	TotalCount    int64       `json:"total_count"`
	CustomerCount int64       `json:"customer_count"`
}

type CommissionStatisticsData struct {
	AgentCount  int64 `json:"agent_count"`
	MasterCount int64 `json:"master_count"`
}

type DepositListInput struct {
	UserCode          *int64  `json:"user_code,omitempty" jsonschema:"Optional user code, at least 1"`
	Status            *int    `json:"status,omitempty" jsonschema:"Optional deposit status: 0, 4, or 5"`
	DepositStatus     *string `json:"deposit_status,omitempty" jsonschema:"Optional first_time or multiple"`
	ReceivedTimeStart *string `json:"received_time_start,omitempty" jsonschema:"Optional received-time start"`
	ReceivedTimeEnd   *string `json:"received_time_end,omitempty" jsonschema:"Optional received-time end"`
	Direction         *int    `json:"direction,omitempty" jsonschema:"Optional direction: 1 or -1"`
	Platform          *string `json:"platform,omitempty" jsonschema:"Optional channel code, at most 64 characters"`
	Limit             *int    `json:"limit,omitempty" jsonschema:"Optional page size between 1 and 100"`
	Offset            *int    `json:"offset,omitempty" jsonschema:"Optional zero-based offset"`
}

type DepositListData struct {
	Items []adminapi.DepositListItem `json:"items"`
}

type CommissionDetailData struct {
	Items []adminapi.CommissionListItem `json:"items"`
}

type CommissionDetailInput struct {
	UserCode  int64  `json:"user_code" jsonschema:"Required agent user code"`
	StartTime string `json:"start_time" jsonschema:"Required start date or datetime"`
	EndTime   string `json:"end_time" jsonschema:"Required end date or datetime"`
	Limit     *int   `json:"limit,omitempty" jsonschema:"Optional page size, at least 1"`
	Offset    *int   `json:"offset,omitempty" jsonschema:"Optional zero-based offset"`
}

func registerBusinessTools(server *mcp.Server, deps Deps) {
	registerDataOverviewTool(server, deps)
	registerDepositStatisticsTool(server, deps)
	registerCommissionStatisticsTool(server, deps)
	registerDepositListTool(server, deps)
	registerCommissionDetailTool(server, deps)
}

func registerDataOverviewTool(server *mcp.Server, deps Deps) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, input DataOverviewInput) (*mcp.CallToolResult, toolruntime.Envelope[DataOverviewData], error) {
		data, err := deps.AdminAPI.DataOverview(ctx, dataOverviewQuery(input))
		if err != nil {
			return nil, upstreamEnvelope[DataOverviewData](err), nil
		}
		return nil, toolruntime.Success(DataOverviewData{
			Customers: CustomerOverview{
				RegisteredCount: data.RegisteredUsers,
				TradingCount:    data.TradingUsers,
			},
			Trading: TradingOverview{
				Lots:       normalizedNumber(data.TradingLots),
				Amount:     normalizedNumber(data.TradingAmount),
				OrderCount: data.OrderCount,
				Currency:   usdtCurrency,
			},
			Deposits: DepositOverview{
				TotalAmount:               normalizedNumber(data.DepositAmount),
				FirstDepositCustomerCount: data.FirstDepositUsers,
				NetAmount:                 normalizedNumber(data.NetDepositAmount),
				WithdrawalAmount:          normalizedNumber(data.WithdrawalAmount),
				Currency:                  usdtCurrency,
			},
			AgentCommission: CommissionAmount{
				TotalAmount:   normalizedNumber(data.IBCommissionAmount),
				CustomerCount: data.IBCommissionCustomerCount,
				Currency:      usdtCurrency,
			},
			MasterProfitSharing: MasterProfitSharingAmount{
				TotalAmount:   normalizedNumber(data.MasterCommissionAmount),
				CustomerCount: data.MasterCommissionCustomerCount,
				Currency:      usdtCurrency,
			},
		}, adminSource("data_overview")), nil
	}

	registerFinancialTool(server, deps, definition(
		"admin_api_data_overview",
		"Admin API Data Overview",
		"Gets registration, trading, deposit, agent commission, and master profit-sharing figures for one period. Amounts are USDT.",
	), handler, []string{depositsPermission, commissionsPermission})
}

func registerDepositStatisticsTool(server *mcp.Server, deps Deps) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, input DateRangeInput) (*mcp.CallToolResult, toolruntime.Envelope[DepositStatisticsData], error) {
		data, err := deps.AdminAPI.DepositStatistics(ctx, timeRangeQuery(input))
		if err != nil {
			return nil, upstreamEnvelope[DepositStatisticsData](err), nil
		}

		chains := make([]DepositChainStatistics, 0, len(data.ChainStatistics))
		for _, chain := range data.ChainStatistics {
			chains = append(chains, DepositChainStatistics{
				ChainID:       chain.ChainID,
				ChainName:     chain.ChainName,
				TotalAmount:   normalizedNumber(chain.TotalAmount),
				Currency:      usdtCurrency,
				TotalCount:    chain.TotalCount,
				CustomerCount: chain.CustomerCount,
			})
		}
		return nil, toolruntime.Success(DepositStatisticsData{
			TotalCount:    data.TotalCount,
			CustomerCount: data.CustomerCount,
			Chains:        chains,
		}, adminSource("deposits/statistics")), nil
	}

	registerFinancialTool(server, deps, definition(
		"admin_api_deposits_statistics",
		"Admin API Deposit Statistics",
		"Gets successful deposit totals and per-chain amount, count, and customer statistics. Amounts are USDT.",
	), handler, []string{depositsPermission})
}

func registerCommissionStatisticsTool(server *mcp.Server, deps Deps) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, input DateRangeInput) (*mcp.CallToolResult, toolruntime.Envelope[CommissionStatisticsData], error) {
		data, err := deps.AdminAPI.CommissionStatistics(ctx, timeRangeQuery(input))
		if err != nil {
			return nil, upstreamEnvelope[CommissionStatisticsData](err), nil
		}
		return nil, toolruntime.Success(CommissionStatisticsData{
			AgentCount:  data.IBCount,
			MasterCount: data.MasterCount,
		}, adminSource("commission/statistics")), nil
	}

	registerFinancialTool(server, deps, definition(
		"admin_api_commission_statistics",
		"Admin API Commission Statistics",
		"Gets the number of agents paid commission and masters paid profit sharing in one period.",
	), handler, []string{commissionsPermission})
}

func registerDepositListTool(server *mcp.Server, deps Deps) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, input DepositListInput) (*mcp.CallToolResult, toolruntime.Envelope[DepositListData], error) {
		query := depositListQuery(input)
		result, err := deps.AdminAPI.DepositList(ctx, query)
		if err != nil {
			return nil, upstreamEnvelope[DepositListData](err), nil
		}

		items := result.Items
		if items == nil {
			items = []adminapi.DepositListItem{}
		}
		envelope := toolruntime.Success(DepositListData{Items: items}, adminSource("deposits"))
		envelope.Pagination = &toolruntime.Pagination{
			Limit:  query.Limit,
			Offset: query.Offset,
			Total:  int(result.TotalCount),
		}
		return nil, envelope, nil
	}

	registerFinancialTool(server, deps, definition(
		"admin_api_deposits_list",
		"Admin API Deposit List",
		"Lists admin-api deposits with optional filters and offset pagination.",
	), handler, []string{depositsPermission})
}

func registerCommissionDetailTool(server *mcp.Server, deps Deps) {
	handler := func(ctx context.Context, _ *mcp.CallToolRequest, input CommissionDetailInput) (*mcp.CallToolResult, toolruntime.Envelope[CommissionDetailData], error) {
		query := commissionListQuery(input)
		result, err := deps.AdminAPI.CommissionList(ctx, query)
		if err != nil {
			return nil, upstreamEnvelope[CommissionDetailData](err), nil
		}
		if result.Items == nil {
			result.Items = []adminapi.CommissionListItem{}
		}
		envelope := toolruntime.Success(CommissionDetailData{Items: result.Items}, adminSource("ib/commissions"))
		envelope.Pagination = &toolruntime.Pagination{
			Limit:  query.Limit,
			Offset: query.Offset,
			Total:  int(result.TotalCount),
		}
		return nil, envelope, nil
	}

	registerFinancialTool(server, deps, definition(
		"admin_api_commissions_agent_details",
		"Admin API Agent Commission Details",
		"Lists settled commission amount and settlement time for one agent.",
	), handler, []string{commissionsPermission})
}

func registerFinancialTool[In, Out any](
	server *mcp.Server,
	deps Deps,
	definition registry.Definition,
	handler mcp.ToolHandlerFor[In, toolruntime.Envelope[Out]],
	requiredPermissions []string,
) {
	definition.Version = "1.0.0"
	definition.Owner = "ai-copilot-platform"
	definition.RequiredPermissions = requiredPermissions
	definition.SensitivityLevel = "confidential_financial"
	definition.Timeout = deps.Timeout.String()
	definition.AuditPolicy = "metadata_only"
	if err := deps.Registry.Register(definition); err != nil {
		panic(err)
	}

	outputSchema, err := financialOutputSchema[Out]()
	if err != nil {
		panic(err)
	}
	tool := &mcp.Tool{
		Name:        definition.Name,
		Title:       definition.Title,
		Description: definition.Description,
	}
	if outputSchema != nil {
		tool.OutputSchema = outputSchema
	}
	mcp.AddTool(server, tool, handler)
}

func financialOutputSchema[Out any]() (*jsonschema.Schema, error) {
	return jsonschema.For[toolruntime.Envelope[Out]](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[json.Number](): {Type: "number"},
		},
	})
}

func definition(name, title, description string) registry.Definition {
	return registry.Definition{
		Name:        name,
		Title:       title,
		Description: description,
	}
}

func dataOverviewQuery(input DataOverviewInput) adminapi.DataOverviewQuery {
	return adminapi.DataOverviewQuery{
		DateStart:         input.DateStart,
		DateEnd:           input.DateEnd,
		TradeAccountType:  input.TradeAccountType,
		CustomerType:      input.CustomerType,
		SourceTradingType: input.SourceTradingType,
		ChartDateType:     input.ChartDateType,
	}
}

func depositListQuery(input DepositListInput) adminapi.DepositListQuery {
	query := adminapi.DepositListQuery{
		UserCode:          input.UserCode,
		Status:            input.Status,
		DepositStatus:     input.DepositStatus,
		ReceivedTimeStart: input.ReceivedTimeStart,
		ReceivedTimeEnd:   input.ReceivedTimeEnd,
		Direction:         input.Direction,
		Platform:          input.Platform,
		Limit:             20,
		Offset:            0,
	}
	if input.Limit != nil {
		query.Limit = *input.Limit
	}
	if input.Offset != nil {
		query.Offset = *input.Offset
	}
	return query
}

func timeRangeQuery(input DateRangeInput) adminapi.TimeRangeQuery {
	return adminapi.TimeRangeQuery{StartTime: input.StartTime, EndTime: input.EndTime}
}

func normalizedNumber(value json.Number) json.Number {
	if value == "" {
		return json.Number("0")
	}
	return value
}

func commissionListQuery(input CommissionDetailInput) adminapi.CommissionListQuery {
	query := adminapi.CommissionListQuery{
		UserCode:  input.UserCode,
		StartTime: input.StartTime,
		EndTime:   input.EndTime,
		Limit:     20,
		Offset:    0,
	}
	if input.Limit != nil {
		query.Limit = *input.Limit
	}
	if input.Offset != nil {
		query.Offset = *input.Offset
	}
	return query
}

func upstreamEnvelope[T any](err error) toolruntime.Envelope[T] {
	var upstream *adminapi.Error
	if errors.As(err, &upstream) {
		return toolruntime.Failure[T](string(upstream.Kind), upstream.Message, upstream.Retryable())
	}
	return toolruntime.Failure[T]("UPSTREAM_ERROR", "admin-api request failed", true)
}

func adminSource(resource string) toolruntime.Source {
	return toolruntime.Source{
		Service:    "admin-api",
		Resource:   resource,
		ObservedAt: time.Now().UTC(),
	}
}
