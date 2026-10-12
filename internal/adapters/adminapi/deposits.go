package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

const (
	depositStatusFirstTime = "first_time"
	depositStatusMultiple  = "multiple"

	depositStatusPending  = 0
	depositStatusApproved = 4
	depositStatusFailed   = 5

	directionDeposit  = 1
	directionWithdraw = -1

	defaultDepositListLimit = 20
	maxDepositListLimit     = 100
)

type DepositListQuery struct {
	UserCode          *int64
	Status            *int
	DepositStatus     *string
	ReceivedTimeStart *string
	ReceivedTimeEnd   *string
	Direction         *int
	Platform          *string
	Limit             int
	Offset            int
}

type DepositListResult struct {
	Items      []DepositListItem `json:"data"`
	TotalCount int64             `json:"total_count"`
}

type DepositStatisticsQuery = TimeRangeQuery

type DepositStatistics struct {
	TotalCount      int64            `json:"total_count"`
	CustomerCount   int64            `json:"customer_count"`
	ChainStatistics []ChainStatistic `json:"chain_statistics"`
}

type ChainStatistic struct {
	ChainID       string      `json:"chain_id"`
	ChainName     string      `json:"chain_name"`
	TotalAmount   json.Number `json:"total_amount"`
	TotalCount    int64       `json:"total_count"`
	CustomerCount int64       `json:"customer_count"`
}

type DepositListItem struct {
	ID                int64         `json:"id"`
	UserCode          int64         `json:"user_code"`
	OrderNo           string        `json:"order_no"`
	DepositTypeText   string        `json:"deposit_type_text"`
	Platform          string        `json:"platform"`
	PlatformText      string        `json:"platform_text"`
	ChannelText       string        `json:"channel_text"`
	Currency          string        `json:"currency"`
	ApplyAmount       json.Number   `json:"apply_amount"`
	FeeAmount         json.Number   `json:"fee_amount"`
	PayCurrency       *string       `json:"pay_currency"`
	PayAmount         *json.Number  `json:"pay_amount"`
	Rate              *json.Number  `json:"rate"`
	ReceivedAt        *string       `json:"received_at"`
	ArrivalTime       *string       `json:"arrival_time"`
	TxHash            string        `json:"tx_hash"`
	Network           string        `json:"network"`
	NetworkText       string        `json:"network_text"`
	Status            DepositStatus `json:"status"`
	StatusText        string        `json:"status_text"`
	DepositStatusText string        `json:"deposit_status_text"`
}

type DepositStatus struct {
	Code          int    `json:"code"`
	Text          string `json:"text"`
	RiskStatus    int    `json:"risk_status"`
	FinanceStatus int    `json:"finance_status"`
}

func (q DepositListQuery) Validate() error {
	if q.UserCode != nil && *q.UserCode < 1 {
		return invalidArgument("user_code must be at least 1")
	}
	if q.Status != nil {
		switch *q.Status {
		case depositStatusPending, depositStatusApproved, depositStatusFailed:
		default:
			return invalidArgument("status must be one of [0 4 5]")
		}
	}
	if q.DepositStatus != nil {
		switch *q.DepositStatus {
		case depositStatusFirstTime, depositStatusMultiple:
		default:
			return invalidArgument("deposit_status must be first_time or multiple")
		}
	}
	if err := validateOptionalDateTime(q.ReceivedTimeStart, "received_time_start"); err != nil {
		return err
	}
	if err := validateOptionalDateTime(q.ReceivedTimeEnd, "received_time_end"); err != nil {
		return err
	}
	if q.ReceivedTimeStart != nil && q.ReceivedTimeEnd != nil && *q.ReceivedTimeStart > *q.ReceivedTimeEnd {
		return invalidArgument("received_time_start must not be after received_time_end")
	}
	if q.Direction != nil {
		switch *q.Direction {
		case directionDeposit, directionWithdraw:
		default:
			return invalidArgument("direction must be 1 or -1")
		}
	}
	if q.Platform != nil && len(*q.Platform) > 64 {
		return invalidArgument("platform must not exceed 64 characters")
	}
	if q.Limit < 1 || q.Limit > maxDepositListLimit {
		return invalidArgument(fmt.Sprintf("limit must be between 1 and %d", maxDepositListLimit))
	}
	if q.Offset < 0 {
		return invalidArgument("offset must be at least 0")
	}
	return nil
}

func (q DepositListQuery) values() url.Values {
	values := url.Values{
		"limit":  {strconv.Itoa(q.Limit)},
		"offset": {strconv.Itoa(q.Offset)},
	}
	if q.UserCode != nil {
		values.Set("user_code", strconv.FormatInt(*q.UserCode, 10))
	}
	if q.Status != nil {
		values.Set("status", strconv.Itoa(*q.Status))
	}
	setOptionalString(values, "deposit_status", q.DepositStatus)
	setOptionalString(values, "received_time_start", q.ReceivedTimeStart)
	setOptionalString(values, "received_time_end", q.ReceivedTimeEnd)
	if q.Direction != nil {
		values.Set("direction", strconv.Itoa(*q.Direction))
	}
	setOptionalString(values, "platform", q.Platform)
	return values
}

func (c *Client) DepositList(ctx context.Context, query DepositListQuery) (DepositListResult, error) {
	if query.Limit == 0 {
		query.Limit = defaultDepositListLimit
	}
	if err := query.Validate(); err != nil {
		return DepositListResult{}, err
	}
	return get[DepositListResult](ctx, c, "/deposits", query.values())
}

func (c *Client) DepositStatistics(ctx context.Context, query DepositStatisticsQuery) (DepositStatistics, error) {
	if err := query.Validate(); err != nil {
		return DepositStatistics{}, err
	}
	return get[DepositStatistics](ctx, c, "/deposits/statistics", query.Values())
}

func validateOptionalDateTime(value *string, field string) error {
	if value == nil {
		return nil
	}
	layouts := []string{time.DateOnly, time.DateTime}
	for _, layout := range layouts {
		if _, err := time.Parse(layout, *value); err == nil {
			return nil
		}
	}
	return invalidArgument(fmt.Sprintf("%s must use YYYY-MM-DD or YYYY-MM-DD HH:MM:SS", field))
}

func setOptionalString(values url.Values, key string, value *string) {
	if value != nil {
		values.Set(key, *value)
	}
}
