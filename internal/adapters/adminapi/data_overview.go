package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

const (
	tradeAccountTypeReal  = "real"
	tradeAccountTypeTrial = "trial"

	customerTypeAll   = "all"
	customerTypeAgent = "ib"

	sourceTradingTypeLot        = "lot"
	sourceTradingTypeAmount     = "amount"
	sourceTradingTypeOrderCount = "order_count"

	chartDateTypeDay   = "day"
	chartDateTypeWeek  = "week"
	chartDateTypeMonth = "month"
)

type DataOverviewQuery struct {
	DateStart         string
	DateEnd           string
	TradeAccountType  string
	CustomerType      string
	SourceTradingType string
	ChartDateType     string
}

type DataOverview struct {
	RegisteredUsers               int64       `json:"registered_users"`
	FirstDepositUsers             int64       `json:"first_deposit_users"`
	TradingUsers                  int64       `json:"trading_users"`
	TradingLots                   json.Number `json:"trading_lots"`
	TradingAmount                 json.Number `json:"trading_amount"`
	OrderCount                    int64       `json:"order_count"`
	DepositAmount                 json.Number `json:"deposit_amount"`
	WithdrawalAmount              json.Number `json:"withdrawal_amount"`
	NetDepositAmount              json.Number `json:"net_deposit_amount"`
	IBCommissionAmount            json.Number `json:"ib_commission_amount"`
	IBCommissionCustomerCount     int64       `json:"ib_commission_customer_count"`
	MasterCommissionAmount        json.Number `json:"master_commission_amount"`
	MasterCommissionCustomerCount int64       `json:"master_commission_customer_count"`
}

func (q DataOverviewQuery) Validate() error {
	if err := validateDate(q.DateStart, "date_start"); err != nil {
		return err
	}
	if err := validateDate(q.DateEnd, "date_end"); err != nil {
		return err
	}
	if q.DateStart > q.DateEnd {
		return invalidArgument("date_start must not be after date_end")
	}
	if err := validateOneOf(q.TradeAccountType, "trade_account_type",
		tradeAccountTypeReal, tradeAccountTypeTrial); err != nil {
		return err
	}
	if err := validateOneOf(q.CustomerType, "customer_type",
		customerTypeAll, customerTypeAgent); err != nil {
		return err
	}
	if err := validateOneOf(q.SourceTradingType, "source_trading_type",
		sourceTradingTypeLot, sourceTradingTypeAmount, sourceTradingTypeOrderCount); err != nil {
		return err
	}
	return validateOneOf(q.ChartDateType, "chart_date_type",
		chartDateTypeDay, chartDateTypeWeek, chartDateTypeMonth)
}

func (q DataOverviewQuery) values() url.Values {
	return url.Values{
		"date_start":          {q.DateStart},
		"date_end":            {q.DateEnd},
		"trade_account_type":  {q.TradeAccountType},
		"customer_type":       {q.CustomerType},
		"source_trading_type": {q.SourceTradingType},
		"chart_date_type":     {q.ChartDateType},
	}
}

func (c *Client) DataOverview(ctx context.Context, query DataOverviewQuery) (DataOverview, error) {
	if err := query.Validate(); err != nil {
		return DataOverview{}, err
	}
	return get[DataOverview](ctx, c, "/data_overview", query.values())
}

func validateDate(value, field string) error {
	if _, err := time.Parse(time.DateOnly, value); err != nil {
		return invalidArgument(fmt.Sprintf("%s must use YYYY-MM-DD", field))
	}
	return nil
}

func validateOneOf(value, field string, allowed ...string) error {
	for _, item := range allowed {
		if value == item {
			return nil
		}
	}
	return invalidArgument(fmt.Sprintf("%s must be one of %v", field, allowed))
}
