package navigation_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Global-MongoX/internal-mcp-server/internal/navigation"
)

func find(t *testing.T, manifest *navigation.Manifest, request navigation.FindRequest) navigation.FindResult {
	t.Helper()
	result, err := manifest.Find(request)
	if err != nil {
		t.Fatalf("find %+v: %v", request, err)
	}
	return result
}

func ids(result navigation.FindResult) []string {
	out := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		out = append(out, match.ID)
	}
	return out
}

// The golden table. The scoring constants in search.go mean nothing on their
// own -- this is what they are tuned against, so change them together.
func TestFindGoldenQueries(t *testing.T) {
	manifest := load(t)

	tests := []struct {
		name     string
		query    string
		first    string   // ranked first, when it matters
		contains []string // present anywhere in the result
		absent   []string
		gaps     []string
		want     int // exact match count, -1 to skip
	}{
		{
			name:  "exact keyword ranks first",
			query: "入金",
			first: "finance.cash.deposit",
			want:  -1,
		},
		{
			name:     "a keyword shared by two pages returns both",
			query:    "出金审核",
			contains: []string{"finance.cash.withdrawRisk", "finance.cash.withdrawFinance"},
			want:     2,
		},
		{
			// The documented 用户 ambiguity: end customers vs admin accounts.
			// Both sides carry disambiguating keywords, so both must surface.
			name:     "user ambiguity surfaces both sides",
			query:    "用户",
			first:    "customer.manage.list",
			contains: []string{"customer.manage.list", "settings.users.settings"},
			want:     -1,
		},
		{
			name:     "hidden pages rank below their list page",
			query:    "角色",
			first:    "settings.roles.settings",
			contains: []string{"settings.roles.settings:edit"},
			want:     -1,
		},
		{
			name:     "a hidden page is still reachable by its own title",
			query:    "修改角色",
			contains: []string{"settings.roles.settings:edit"},
			want:     1,
		},
		{
			name:     "english i18n title",
			query:    "Ad List",
			contains: []string{"content.ads.list"},
			want:     1,
		},
		{
			name:     "japanese i18n title",
			query:    "ブラックリスト",
			contains: []string{"customer.special.blacklist"},
			want:     1,
		},
		{
			name:  "a natural sentence still finds the page",
			query: "入金记录在后台哪里看？",
			first: "finance.cash.deposit",
			want:  1,
		},
		{
			// Nothing in the console does this, and saying so beats "not found".
			name:  "a documented absence returns no page but an explanation",
			query: "后台的 KYC 审核在哪",
			gaps:  []string{"KYC / 实名认证"},
			want:  0,
		},
		{
			// Gaps come back alongside matches, not only instead of them.
			name:     "a gap accompanies a partial match",
			query:    "怎么给客户发公告",
			contains: []string{"customer.manage.list"},
			gaps:     []string{"公告管理"},
			want:     -1,
		},
		{
			// The threshold exists so one character does not return a third of
			// the manifest.
			name:  "a single character is below the threshold",
			query: "入",
			want:  0,
		},
		{
			name:   "commission records, not the rule config",
			query:  "佣金记录在哪",
			first:  "agents.commission.records",
			absent: []string{"agents.commission.configs"},
			want:   -1,
		},
		{
			name:  "profit sharing is not agent commission",
			query: "分润",
			first: "masters.profitSharing",
			want:  1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := find(t, manifest, navigation.FindRequest{Query: test.query})
			got := ids(result)

			if test.want >= 0 && result.Total != test.want {
				t.Fatalf("total = %d (%v), want %d", result.Total, got, test.want)
			}
			if test.first != "" && (len(got) == 0 || got[0] != test.first) {
				t.Errorf("ranked first = %v, want %s", got, test.first)
			}
			for _, id := range test.contains {
				if !slices.Contains(got, id) {
					t.Errorf("matches = %v, want it to contain %s", got, id)
				}
			}
			for _, id := range test.absent {
				if slices.Contains(got, id) {
					t.Errorf("matches = %v, want it to omit %s", got, id)
				}
			}
			var gapTopics []string
			for _, gap := range result.KnownGaps {
				gapTopics = append(gapTopics, gap.Topic)
			}
			for _, topic := range test.gaps {
				if !slices.Contains(gapTopics, topic) {
					t.Errorf("gaps = %v, want it to contain %q", gapTopics, topic)
				}
			}
			if len(test.gaps) == 0 && len(gapTopics) > 0 {
				t.Errorf("gaps = %v, want none", gapTopics)
			}
		})
	}
}

func TestFindIsDeterministic(t *testing.T) {
	manifest := load(t)
	first := ids(find(t, manifest, navigation.FindRequest{Query: "出金", Limit: navigation.MaxLimit}))
	for range 5 {
		if got := ids(find(t, manifest, navigation.FindRequest{Query: "出金", Limit: navigation.MaxLimit})); !slices.Equal(got, first) {
			t.Fatalf("ranking = %v, want %v", got, first)
		}
	}
}

func TestFindReportsTruncation(t *testing.T) {
	manifest := load(t)
	result := find(t, manifest, navigation.FindRequest{Query: "管理", Limit: 2})
	if len(result.Matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(result.Matches))
	}
	if result.Total <= 2 {
		t.Fatalf("total = %d, want it to report the untruncated count", result.Total)
	}
}

func TestFindByIDReturnsOnlyThatPage(t *testing.T) {
	result := find(t, load(t), navigation.FindRequest{Query: "finance.cash.deposit"})
	if len(result.Matches) != 1 || result.Matches[0].ID != "finance.cash.deposit" {
		t.Fatalf("matches = %v, want just the exact id", ids(result))
	}
	if !slices.Contains(result.Matches[0].MatchedOn, "id") {
		t.Errorf("matched_on = %v, want it to record the id hit", result.Matches[0].MatchedOn)
	}
}

// Browsing a whole menu group is what an operator asking "资金管理下面有哪些页面"
// needs, and it is the only case where an empty query is allowed.
func TestFindBrowsesAMenuGroup(t *testing.T) {
	manifest := load(t)
	result := find(t, manifest, navigation.FindRequest{MenuGroup: "资金管理", Limit: navigation.MaxLimit})
	if result.Total != 7 {
		t.Fatalf("total = %d (%v), want 7", result.Total, ids(result))
	}
	for _, match := range result.Matches {
		if match.MenuGroup != "资金管理" {
			t.Errorf("page %s is in group %q", match.ID, match.MenuGroup)
		}
	}
	if result.Matches[0].ID != "finance.cash.deposit" {
		t.Errorf("browse order = %v, want manifest order", ids(result))
	}
}

func TestFindFiltersByMenuGroup(t *testing.T) {
	manifest := load(t)
	result := find(t, manifest, navigation.FindRequest{Query: "列表", MenuGroup: "订单管理", Limit: navigation.MaxLimit})
	if len(result.Matches) == 0 {
		t.Fatal("no matches")
	}
	for _, match := range result.Matches {
		if match.MenuGroup != "订单管理" {
			t.Errorf("page %s leaked in from %q", match.ID, match.MenuGroup)
		}
	}
}

func TestFindRejectsBadInput(t *testing.T) {
	manifest := load(t)
	tests := map[string]struct {
		request navigation.FindRequest
		want    error
	}{
		"empty":            {request: navigation.FindRequest{Query: "   "}, want: navigation.ErrEmptyQuery},
		"punctuation only": {request: navigation.FindRequest{Query: "？？？"}, want: navigation.ErrEmptyQuery},
		"too long":         {request: navigation.FindRequest{Query: strings.Repeat("入", 81)}, want: navigation.ErrQueryTooLong},
		"unknown group":    {request: navigation.FindRequest{Query: "入金", MenuGroup: "不存在的菜单"}, want: navigation.ErrUnknownMenuGroup},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := manifest.Find(test.request); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	if _, err := manifest.Find(navigation.FindRequest{Query: strings.Repeat("入", 80)}); err != nil {
		t.Errorf("80 runes should be accepted: %v", err)
	}
}

// api_endpoints is indexed so "data_overview 是哪个页面" works, but never
// returned: a model that sees endpoint names starts implying it can call them.
func TestFindMatchesAPIEndpointsWithoutReturningThem(t *testing.T) {
	result := find(t, load(t), navigation.FindRequest{Query: "data_overview"})
	if len(result.Matches) == 0 || result.Matches[0].ID != "customer.dashboard.bi" {
		t.Fatalf("matches = %v, want the dashboard page", ids(result))
	}
	for _, match := range result.Matches {
		if strings.Contains(strings.Join(match.Capabilities, " "), "data_overview") {
			t.Errorf("page %s leaked an endpoint name into capabilities", match.ID)
		}
	}
}
