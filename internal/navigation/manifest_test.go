package navigation_test

import (
	"strings"
	"testing"

	"github.com/Global-MongoX/internal-mcp-server/internal/navigation"
	navdata "github.com/Global-MongoX/internal-mcp-server/knowledge/navigation"
)

const testBaseURL = "https://console.example.test"

func embeddedManifest(t *testing.T) []byte {
	t.Helper()
	return navdata.PagesYAML
}

func load(t *testing.T) *navigation.Manifest {
	t.Helper()
	manifest, err := navigation.Load(navdata.PagesYAML, testBaseURL)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	return manifest
}

// These assertions are about the real embedded manifest, not a fixture: if the
// generator or admin-frontend changes shape, this is where it surfaces.
func TestLoadEmbeddedManifest(t *testing.T) {
	manifest := load(t)
	pages := manifest.Pages()

	if len(pages) != 54 {
		t.Fatalf("pages = %d, want 54", len(pages))
	}
	hidden, withParams := 0, 0
	ids := make(map[string]bool, len(pages))
	for i := range pages {
		page := &pages[i]
		if ids[page.ID] {
			t.Errorf("duplicate page id %q", page.ID)
		}
		ids[page.ID] = true
		if page.Hidden {
			hidden++
		}
		if len(page.EntityParams) > 0 {
			withParams++
		}
		if len(page.MenuPath) == 0 {
			t.Errorf("page %q has an empty menu_path", page.ID)
		}
		if len(page.Keywords) == 0 {
			t.Errorf("page %q has no keywords, so nothing can find it", page.ID)
		}
		if !strings.HasPrefix(page.Route, "/") {
			t.Errorf("page %q route %q must start with /", page.ID, page.Route)
		}
		if strings.Contains(page.Route, "..") || strings.Contains(page.Route, "://") ||
			strings.ContainsAny(page.Route, "#? \t") {
			t.Errorf("page %q route %q is not a plain path", page.ID, page.Route)
		}
		for _, param := range page.EntityParams {
			if !strings.Contains(page.Route, ":"+param) {
				t.Errorf("page %q declares entity param %q missing from route %q", page.ID, param, page.Route)
			}
		}
	}
	if hidden != 6 {
		t.Errorf("hidden pages = %d, want 6", hidden)
	}
	// Record-level deep links barely exist in this console; only four routes
	// take a dynamic segment. Everything else gets a page-level link.
	if withParams != 4 {
		t.Errorf("pages with entity params = %d, want 4", withParams)
	}

	gaps := manifest.KnownGaps()
	if len(gaps) != 6 {
		t.Fatalf("known gaps = %d, want 6", len(gaps))
	}
	for _, gap := range gaps {
		if gap.Topic == "" || gap.Note == "" {
			t.Errorf("gap %+v is missing a topic or note", gap)
		}
		if len(gap.Keywords) == 0 {
			t.Errorf("gap %q has no keywords, so nothing can match it", gap.Topic)
		}
	}
}

// The nine groups come from menu_path[0]. A rename in admin-frontend should
// fail here rather than silently change what menu_group accepts.
func TestMenuGroups(t *testing.T) {
	got := load(t).MenuGroups()
	want := []string{"客户管理", "订单管理", "内容管理", "市场活动", "资金管理", "代理管理", "高手管理", "品种", "设置"}
	if len(got) != len(want) {
		t.Fatalf("menu groups = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("menu groups = %v, want %v", got, want)
		}
	}
}

func TestLoadRequiresConsoleBaseURL(t *testing.T) {
	if _, err := navigation.Load(navdata.PagesYAML, "  "); err == nil {
		t.Fatal("empty base URL was accepted")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	manifest := []byte("known_gaps: []\npages:\n  - id: a\n    title: A\n    route: /a\n    menu_path: [x]\n    surprise: true\n")
	_, err := navigation.Load(manifest, testBaseURL)
	if err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("error = %v, want an unknown-field failure naming surprise", err)
	}
}

// A malformed manifest must stop the process, because every navigation answer
// would otherwise be a link built from data nobody checked.
func TestLoadRejectsUnsafeRoutes(t *testing.T) {
	cases := map[string]string{
		"relative route":     "  - id: a\n    title: A\n    route: a/b\n    menu_path: [x]\n",
		"traversal":          "  - id: a\n    title: A\n    route: /a/../b\n    menu_path: [x]\n",
		"absolute url":       "  - id: a\n    title: A\n    route: /x\n    menu_path: [x]\n  - id: b\n    title: B\n    route: https://evil.test/x\n    menu_path: [x]\n",
		"query string":       "  - id: a\n    title: A\n    route: /a?next=1\n    menu_path: [x]\n",
		"empty menu path":    "  - id: a\n    title: A\n    route: /a\n    menu_path: []\n",
		"undeclared param":   "  - id: a\n    title: A\n    route: /a\n    menu_path: [x]\n    entity_params: [roleId]\n",
		"duplicate page ids": "  - id: a\n    title: A\n    route: /a\n    menu_path: [x]\n  - id: a\n    title: A2\n    route: /a2\n    menu_path: [x]\n",
	}
	for name, pages := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := navigation.Load([]byte("pages:\n"+pages), testBaseURL); err == nil {
				t.Fatal("manifest was accepted")
			}
		})
	}
}
