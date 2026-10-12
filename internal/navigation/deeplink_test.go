package navigation_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Global-MongoX/internal-mcp-server/internal/navigation"
)

func TestGetBuildsAbsoluteLinks(t *testing.T) {
	manifest := load(t)

	page, gaps, err := manifest.Get("finance.cash.deposit", nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.URL != testBaseURL+"/finance/deposit" {
		t.Errorf("url = %q", page.URL)
	}
	if page.DeepLinkTemplate != page.URL {
		t.Errorf("template = %q, want it to equal the url when nothing is unresolved", page.DeepLinkTemplate)
	}
	if len(page.RequiresEntity) != 0 {
		t.Errorf("requires entity = %v, want none", page.RequiresEntity)
	}
	if page.MenuKey != "finance.cash.deposit" {
		t.Errorf("menu key = %q", page.MenuKey)
	}
	if len(gaps) != 0 {
		t.Errorf("gaps = %v, want none for a plain list page", gaps)
	}
}

// A base URL with a path prefix is legal; a trailing slash must not produce a
// double slash.
func TestGetTrimsTrailingSlashOnBaseURL(t *testing.T) {
	manifest, err := navigation.Load(embeddedManifest(t), "https://console.test/admin/")
	if err != nil {
		t.Fatal(err)
	}
	page, _, err := manifest.Get("finance.cash.deposit", nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.URL != "https://console.test/admin/finance/deposit" {
		t.Fatalf("url = %q", page.URL)
	}
}

// The four dynamic routes are the only record-level deep links in the console.
// Without values they advertise what they need rather than hand back a link
// with a literal :roleId in it.
func TestGetWithoutEntityValues(t *testing.T) {
	page, _, err := load(t).Get("settings.roles.settings:edit", nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.URL != "" {
		t.Errorf("url = %q, want empty until an entity value is supplied", page.URL)
	}
	if page.DeepLinkTemplate != testBaseURL+"/roles/edit/:roleId" {
		t.Errorf("template = %q", page.DeepLinkTemplate)
	}
	if len(page.RequiresEntity) != 1 || page.RequiresEntity[0] != "roleId" {
		t.Errorf("requires entity = %v, want [roleId]", page.RequiresEntity)
	}
}

func TestGetSubstitutesEntityValues(t *testing.T) {
	page, _, err := load(t).Get("settings.roles.settings:edit", map[string]string{"roleId": "12"})
	if err != nil {
		t.Fatal(err)
	}
	if page.URL != testBaseURL+"/roles/edit/12" {
		t.Fatalf("url = %q", page.URL)
	}
}

// The charset restriction plus PathEscape is what stops a value typed into a
// chat box from turning a console link into something else.
func TestGetRejectsUnsafeEntityValues(t *testing.T) {
	manifest := load(t)
	cases := map[string]map[string]string{
		"traversal":         {"roleId": "../../admin"},
		"encoded slash":     {"roleId": "a%2fb"},
		"slash":             {"roleId": "a/b"},
		"query string":      {"roleId": "1?next=x"},
		"fragment":          {"roleId": "1#x"},
		"empty":             {"roleId": ""},
		"too long":          {"roleId": strings.Repeat("a", 65)},
		"wrong param name":  {"userId": "12"},
		"unsafe param name": {"role id": "12"},
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := manifest.Get("settings.roles.settings:edit", values); err == nil {
				t.Fatalf("values %v were accepted", values)
			}
		})
	}
}

func TestGetRejectsValuesForAStaticPage(t *testing.T) {
	if _, _, err := load(t).Get("finance.cash.deposit", map[string]string{"roleId": "12"}); err == nil {
		t.Fatal("entity params were accepted for a page that takes none")
	}
}

func TestGetRejectsUnknownPage(t *testing.T) {
	_, _, err := load(t).Get("finance.cash.nope", nil)
	if !errors.Is(err, navigation.ErrPageNotFound) {
		t.Fatalf("error = %v, want ErrPageNotFound", err)
	}
}

// api_endpoints and reviewed are indexed but never handed to a caller.
func TestPageViewOmitsInternalFields(t *testing.T) {
	page, _, err := load(t).Get("finance.cash.deposit", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(page.Description+strings.Join(page.Capabilities, " "), "deposits/export") {
		t.Error("an endpoint name leaked into the view")
	}
}
