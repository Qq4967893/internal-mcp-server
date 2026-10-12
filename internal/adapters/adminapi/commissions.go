package adminapi

import (
	"context"
	"net/url"
	"path"
	"strconv"
)

const defaultCommissionListLimit = 20

type CommissionStatisticsQuery = TimeRangeQuery

type CommissionStatistics struct {
	IBCount     int64 `json:"ib_count"`
	MasterCount int64 `json:"master_count"`
}

type CommissionListQuery struct {
	UserCode  int64
	StartTime string
	EndTime   string
	Limit     int
	Offset    int
}

type CommissionListResult struct {
	Items      []CommissionListItem `json:"data"`
	TotalCount int64                `json:"total_count"`
}

type CommissionListItem struct {
	CommissionAmount string `json:"commission_amount"`
	SettledAt        string `json:"settled_at"`
}

func (c *Client) CommissionStatistics(ctx context.Context, query CommissionStatisticsQuery) (CommissionStatistics, error) {
	if err := query.Validate(); err != nil {
		return CommissionStatistics{}, err
	}
	return get[CommissionStatistics](ctx, c, "/commission/statistics", query.Values())
}

func (c *Client) CommissionList(ctx context.Context, query CommissionListQuery) (CommissionListResult, error) {
	if query.Limit == 0 {
		query.Limit = defaultCommissionListLimit
	}
	if query.Offset < 0 {
		return CommissionListResult{}, invalidArgument("offset must be at least 0")
	}
	if query.Limit < 1 {
		return CommissionListResult{}, invalidArgument("limit must be at least 1")
	}
	if err := query.Validate(); err != nil {
		return CommissionListResult{}, err
	}
	endpoint := path.Join("/ib", strconv.FormatInt(query.UserCode, 10), "commissions")
	return get[CommissionListResult](ctx, c, endpoint, query.values())
}

func (q CommissionListQuery) Validate() error {
	if q.UserCode < 1 {
		return invalidArgument("user_code must be at least 1")
	}
	if q.Limit < 1 {
		return invalidArgument("limit must be at least 1")
	}
	if q.Offset < 0 {
		return invalidArgument("offset must be at least 0")
	}
	return q.timeRange().Validate()
}

func (q CommissionListQuery) timeRange() TimeRangeQuery {
	return TimeRangeQuery{StartTime: q.StartTime, EndTime: q.EndTime}
}

func (q CommissionListQuery) values() url.Values {
	values := q.timeRange().Values()
	values.Set("limit", strconv.Itoa(q.Limit))
	values.Set("offset", strconv.Itoa(q.Offset))
	return values
}
