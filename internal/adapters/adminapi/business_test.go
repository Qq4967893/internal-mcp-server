package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientDataOverviewSendsRequiredQueryAndPreservesNumbers(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"is_succ": true,
			"code": 0,
			"data": {
				"first_deposit_users": 7,
				"deposit_amount": 1234.50,
				"net_deposit_amount": 1000.25
			}
		}`))
	}))
	defer server.Close()

	query0 := DataOverviewQuery{
		DateStart:         "2026-09-01",
		DateEnd:           "2026-09-07",
		TradeAccountType:  "real",
		CustomerType:      "ib",
		SourceTradingType: "amount",
		ChartDateType:     "month",
	}
	data, err := newTestClient(t, server, time.Second).DataOverview(context.Background(), query0)
	if err != nil {
		t.Fatal(err)
	}
	wantQuery := "chart_date_type=month&customer_type=ib&date_end=2026-09-07&date_start=2026-09-01&source_trading_type=amount&trade_account_type=real"
	if query != wantQuery {
		t.Errorf("query = %q, want %q", query, wantQuery)
	}
	if data.FirstDepositUsers != 7 || data.DepositAmount.String() != "1234.50" || data.NetDepositAmount.String() != "1000.25" {
		t.Errorf("data = %+v", data)
	}
}

func TestClientDataOverviewValidatesQuery(t *testing.T) {
	valid := DataOverviewQuery{
		DateStart:         "2026-09-01",
		DateEnd:           "2026-09-07",
		TradeAccountType:  "real",
		CustomerType:      "all",
		SourceTradingType: "lot",
		ChartDateType:     "day",
	}
	tests := []struct {
		name   string
		mutate func(*DataOverviewQuery)
	}{
		{name: "bad start", mutate: func(q *DataOverviewQuery) { q.DateStart = "2026/09/01" }},
		{name: "bad end", mutate: func(q *DataOverviewQuery) { q.DateEnd = "" }},
		{name: "reversed", mutate: func(q *DataOverviewQuery) { q.DateStart, q.DateEnd = q.DateEnd, q.DateStart }},
		{name: "bad account", mutate: func(q *DataOverviewQuery) { q.TradeAccountType = "demo" }},
		{name: "bad customer", mutate: func(q *DataOverviewQuery) { q.CustomerType = "partner" }},
		{name: "bad source", mutate: func(q *DataOverviewQuery) { q.SourceTradingType = "profit" }},
		{name: "bad chart", mutate: func(q *DataOverviewQuery) { q.ChartDateType = "year" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := valid
			test.mutate(&query)
			if err := query.Validate(); err == nil {
				t.Fatal("invalid query was accepted")
			}
		})
	}
}

func TestClientDepositListSendsOptionalFiltersAndDefaults(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"is_succ": true,
			"code": 0,
			"data": {
				"data": [{
					"id": 9,
					"user_code": 123,
					"order_no": "order-1",
					"apply_amount": "88.80",
					"network": "eip155:11155111",
					"network_text": "Ethereum Sepolia Testnet(ERC20)",
					"status": {"code": 4, "text": "Success", "risk_status": 1, "finance_status": 2}
				}],
				"total_count": 31
			}
		}`))
	}))
	defer server.Close()

	status := depositStatusApproved
	platform := "cregis"
	result, err := newTestClient(t, server, time.Second).DepositList(context.Background(), DepositListQuery{
		Status:            &status,
		ReceivedTimeStart: strPointer("2026-09-01"),
		Platform:          &platform,
		Limit:             50,
		Offset:            10,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantQuery := "limit=50&offset=10&platform=cregis&received_time_start=2026-09-01&status=4"
	if query != wantQuery {
		t.Errorf("query = %q, want %q", query, wantQuery)
	}
	if result.TotalCount != 31 || len(result.Items) != 1 || result.Items[0].ApplyAmount.String() != "88.80" {
		t.Fatalf("result = %+v", result)
	}
	if result.Items[0].NetworkText != "Ethereum Sepolia Testnet(ERC20)" || result.Items[0].Status.Code != 4 {
		t.Errorf("item = %+v", result.Items[0])
	}

	defaults, err := newTestClient(t, server, time.Second).DepositList(context.Background(), DepositListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if defaults.TotalCount != 31 {
		t.Errorf("defaults result = %+v", defaults)
	}
}

func TestClientDepositListValidatesQuery(t *testing.T) {
	status := 6
	limit := 101
	platform := string(make([]byte, 65))
	start := "2026-09-07"
	end := "2026-09-01"
	tests := []struct {
		name  string
		query DepositListQuery
	}{
		{name: "user code", query: DepositListQuery{UserCode: int64Pointer(0)}},
		{name: "status", query: DepositListQuery{Status: &status}},
		{name: "deposit status", query: DepositListQuery{DepositStatus: strPointer("new")}},
		{name: "bad start", query: DepositListQuery{ReceivedTimeStart: strPointer("09/01/2026")}},
		{name: "bad end", query: DepositListQuery{ReceivedTimeEnd: strPointer("not-a-date")}},
		{name: "reversed time", query: DepositListQuery{ReceivedTimeStart: &start, ReceivedTimeEnd: &end}},
		{name: "direction", query: DepositListQuery{Direction: intPointer(0)}},
		{name: "platform", query: DepositListQuery{Platform: &platform}},
		{name: "limit", query: DepositListQuery{Limit: limit}},
		{name: "offset", query: DepositListQuery{Offset: -1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.query.Validate(); err == nil {
				t.Fatal("invalid query was accepted")
			}
		})
	}
}

func TestTypedRequestMapsFailedEnvelopeAndInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/data_overview" {
			_, _ = w.Write([]byte(`{"is_succ":false,"code":422,"message":"Invalid data"}`))
			return
		}
		_, _ = w.Write([]byte(`{"is_succ":true,"code":0,"data":{"data":"not-a-list"}}`))
	}))
	defer server.Close()
	client := newTestClient(t, server, time.Second)

	overview := DataOverviewQuery{
		DateStart: "2026-09-01", DateEnd: "2026-09-02", TradeAccountType: "real",
		CustomerType: "all", SourceTradingType: "lot", ChartDateType: "day",
	}
	_, err := client.DataOverview(context.Background(), overview)
	var adminErr *Error
	if !errors.As(err, &adminErr) || adminErr.Kind != ErrorKindInvalid {
		t.Fatalf("overview error = %v", err)
	}

	_, err = client.DepositList(context.Background(), DepositListQuery{})
	if !errors.As(err, &adminErr) || adminErr.Kind != ErrorKindUpstream {
		t.Fatalf("deposit error = %v", err)
	}
}

func TestTypedRequestMapsUpstreamStatuses(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		want      ErrorKind
		retryable bool
	}{
		{name: "unauthenticated", status: http.StatusUnauthorized, body: `{"is_succ":false,"code":10000,"message":"login required"}`, want: ErrorKindUnauthenticated},
		{name: "forbidden", status: http.StatusForbidden, body: `{"is_succ":false,"message":"denied"}`, want: ErrorKindForbidden},
		{name: "invalid status", status: http.StatusUnprocessableEntity, body: `{"is_succ":false,"code":422,"message":"bad input"}`, want: ErrorKindInvalid},
		{name: "invalid envelope", status: http.StatusOK, body: `{"is_succ":false,"code":422,"message":"bad input"}`, want: ErrorKindInvalid},
		{name: "rate limited", status: http.StatusTooManyRequests, body: "slow down", want: ErrorKindRateLimited, retryable: true},
		{name: "server error", status: http.StatusInternalServerError, body: "broken", want: ErrorKindUpstream, retryable: true},
		{name: "not found", status: http.StatusNotFound, body: "missing", want: ErrorKindUpstream},
	}

	query := TimeRangeQuery{StartTime: "2026-09-01", EndTime: "2026-09-07"}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			_, err := newTestClient(t, server, time.Second).CommissionStatistics(context.Background(), query)
			var adminErr *Error
			if !errors.As(err, &adminErr) {
				t.Fatalf("error = %T, %v", err, err)
			}
			if adminErr.Kind != test.want || adminErr.Retryable() != test.retryable {
				t.Fatalf("error = %+v, want %s retryable=%t", adminErr, test.want, test.retryable)
			}
		})
	}
}

func TestTypedRequestMapsTransportAndTimeoutErrors(t *testing.T) {
	query := TimeRangeQuery{StartTime: "2026-09-01", EndTime: "2026-09-07"}

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()
	client, err := NewClient(url, time.Second, NewStaticCredentialProvider(Credentials{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CommissionStatistics(context.Background(), query)
	var adminErr *Error
	if !errors.As(err, &adminErr) || adminErr.Kind != ErrorKindUpstream || !adminErr.Retryable() {
		t.Fatalf("transport error = %v", err)
	}

	release := make(chan struct{})
	server = httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	client, err = NewClient(server.URL, time.Millisecond, NewStaticCredentialProvider(Credentials{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CommissionStatistics(context.Background(), query)
	if !errors.As(err, &adminErr) || adminErr.Kind != ErrorKindTimeout || !adminErr.Retryable() {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestClientDepositStatisticsContract(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"is_succ":true,"code":0,"data":{
			"total_count":12,
			"customer_count":8,
			"chain_statistics":[{"chain_id":"eip155:11155111","chain_name":"Ethereum Sepolia Testnet(ERC20)","total_amount":1200.50,"total_count":8,"customer_count":5}]
		}}`))
	}))
	defer server.Close()

	data, err := newTestClient(t, server, time.Second).DepositStatistics(context.Background(), TimeRangeQuery{
		StartTime: "2026-09-01", EndTime: "2026-09-07 23:59:59",
	})
	if err != nil {
		t.Fatal(err)
	}
	if query != "end_time=2026-09-07+23%3A59%3A59&start_time=2026-09-01" {
		t.Errorf("query = %q", query)
	}
	if data.TotalCount != 12 || data.CustomerCount != 8 || len(data.ChainStatistics) != 1 {
		t.Fatalf("data = %+v", data)
	}
	if data.ChainStatistics[0].ChainID != "eip155:11155111" || data.ChainStatistics[0].TotalAmount.String() != "1200.50" {
		t.Errorf("chain = %+v", data.ChainStatistics[0])
	}
}

func TestClientCommissionStatisticsContract(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"is_succ":true,"code":0,"data":{"ib_count":6,"master_count":3}}`))
	}))
	defer server.Close()

	data, err := newTestClient(t, server, time.Second).CommissionStatistics(context.Background(), TimeRangeQuery{
		StartTime: "2026-09-01", EndTime: "2026-09-07",
	})
	if err != nil {
		t.Fatal(err)
	}
	if query != "end_time=2026-09-07&start_time=2026-09-01" {
		t.Errorf("query = %q", query)
	}
	if data.IBCount != 6 || data.MasterCount != 3 {
		t.Errorf("data = %+v", data)
	}
}

func TestClientCommissionListContract(t *testing.T) {
	var path, query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"is_succ":true,"code":0,"data":{"data":[{"commission_amount":"10.50","settled_at":"2026-09-02 11:00:00"}],"total_count":2}}`))
	}))
	defer server.Close()

	data, err := newTestClient(t, server, time.Second).CommissionList(context.Background(), CommissionListQuery{
		UserCode: 123, StartTime: "2026-09-01", EndTime: "2026-09-07",
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/ib/123/commissions" || query != "end_time=2026-09-07&limit=20&offset=0&start_time=2026-09-01" {
		t.Errorf("request = %s?%s", path, query)
	}
	if data.TotalCount != 2 || len(data.Items) != 1 || data.Items[0].CommissionAmount != "10.50" {
		t.Fatalf("data = %+v", data)
	}
}

func TestTimeRangeValidation(t *testing.T) {
	tests := []struct {
		name  string
		query TimeRangeQuery
	}{
		{name: "missing start", query: TimeRangeQuery{EndTime: "2026-09-07"}},
		{name: "bad end", query: TimeRangeQuery{StartTime: "2026-09-01", EndTime: "09/07/2026"}},
		{name: "reversed", query: TimeRangeQuery{StartTime: "2026-09-07", EndTime: "2026-09-01"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.query.Validate(); err == nil {
				t.Fatal("invalid range was accepted")
			}
		})
	}
}

func TestTypedRequestMarshalsNumbersWithoutQuotation(t *testing.T) {
	data := DataOverview{DepositAmount: json.Number("1234.50")}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || !containsSubstring(raw, `"deposit_amount":1234.50`) {
		t.Errorf("marshal = %s", raw)
	}
}

func strPointer(value string) *string { return &value }

func intPointer(value int) *int { return &value }

func int64Pointer(value int64) *int64 { return &value }

func containsSubstring(raw []byte, want string) bool {
	return strings.Contains(string(raw), want)
}

func TestTypedRequestAcceptsSuccessEnvelopeWithoutData(t *testing.T) {
	// A successful call with nothing to return -- an empty result set -- omits
	// the data key; that is not a decode failure.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"is_succ":true,"code":0,"message":"ok"}`))
	}))
	defer server.Close()

	data, err := newTestClient(t, server, time.Second).CommissionStatistics(context.Background(), TimeRangeQuery{
		StartTime: "2026-09-01", EndTime: "2026-09-07",
	})
	if err != nil {
		t.Fatalf("empty data rejected: %v", err)
	}
	if data.IBCount != 0 || data.MasterCount != 0 {
		t.Errorf("data = %+v", data)
	}
}

func TestTypedRequestAcceptsEnvelopeWithoutIsSucc(t *testing.T) {
	// An envelope needs only one of is_succ or code, so code 0 alone is a
	// success and its data must not be discarded.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"ib_count":6,"master_count":3}}`))
	}))
	defer server.Close()

	data, err := newTestClient(t, server, time.Second).CommissionStatistics(context.Background(), TimeRangeQuery{
		StartTime: "2026-09-01", EndTime: "2026-09-07",
	})
	if err != nil {
		t.Fatalf("code-only envelope rejected: %v", err)
	}
	if data.IBCount != 6 || data.MasterCount != 3 {
		t.Errorf("data = %+v", data)
	}
}

func TestTypedRequestDescribesMessagelessFailureOn200(t *testing.T) {
	// The upstream reports the failure in the envelope and returns 200, so the
	// error must not default to the status text ("OK").
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"is_succ":false,"code":5001}`))
	}))
	defer server.Close()

	_, err := newTestClient(t, server, time.Second).CommissionStatistics(context.Background(), TimeRangeQuery{
		StartTime: "2026-09-01", EndTime: "2026-09-07",
	})
	var adminErr *Error
	if !errors.As(err, &adminErr) {
		t.Fatalf("error = %T, %v", err, err)
	}
	if adminErr.Message == "OK" || adminErr.Code != 5001 {
		t.Errorf("error = %+v", adminErr)
	}
}
