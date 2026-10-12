// Package navigation answers "which admin-console page does this?" from the
// manifest generated out of admin-frontend's route table. It never calls
// admin-api: every answer comes from data embedded in the binary at build time.
package navigation

// KnownGap records something the admin console genuinely does not have, so a
// caller can answer "there is no KYC page" instead of "not found". Keywords are
// hand-maintained in pages.overlay.yaml; matching them is exact rather than
// heuristic, because guessing at a four-character topic like 公告管理 either
// misses the real question or fires on every sentence containing 管理.
type KnownGap struct {
	Topic    string   `yaml:"topic"`
	Note     string   `yaml:"note"`
	Keywords []string `yaml:"keywords"`

	terms gapTerms `yaml:"-"`
}

// Page is one admin-console page exactly as the generator emits it.
type Page struct {
	ID           string   `yaml:"id"`
	Title        string   `yaml:"title"`
	Route        string   `yaml:"route"`
	MenuPath     []string `yaml:"menu_path"`
	MenuKey      string   `yaml:"menu_key"`
	Hidden       bool     `yaml:"hidden"`
	APIEndpoints []string `yaml:"api_endpoints"`
	Description  string   `yaml:"description"`
	Keywords     []string `yaml:"keywords"`
	Capabilities []string `yaml:"capabilities"`
	LookupHint   string   `yaml:"lookup_hint"`
	EntityParams []string `yaml:"entity_params"`
	Reviewed     bool     `yaml:"reviewed"`

	// index is the page's position in the manifest. The generator emits in
	// route-table order and writes no timestamp, so this is stable across
	// regenerations and makes ranking a total order.
	index int
	terms pageTerms
}

// MenuGroup is the top-level menu this page hangs under. The manifest has no
// domain taxonomy and inventing one would mean hand-maintaining 54 rows that
// drift; menu_path[0] is real, free and already correct.
func (p *Page) MenuGroup() string {
	if len(p.MenuPath) == 0 {
		return ""
	}
	return p.MenuPath[0]
}

// pageTerms is everything about a page pre-normalised at load time, so a query
// never pays for folding 54 pages worth of text.
type pageTerms struct {
	id           string
	menuKey      string
	keywords     []string // title first, then the manifest keywords
	menuPath     []string
	capabilities []string
	description  string
	lookupHint   string
	apiEndpoints []string

	// keywordText and bodyText back the gram-coverage score. They are one
	// blob each rather than a list because coverage asks "does this gram
	// appear anywhere in the page's vocabulary", not "in which field".
	keywordText string
	bodyText    string
}

type gapTerms struct {
	topic    string
	keywords []string
}

// PageView is one page as handed to a caller: manifest data plus the links this
// server computed. Note what is absent. api_endpoints is indexed but never
// returned (implementation noise, and a model that sees endpoint names starts
// implying it can call them), and so is reviewed (internal QA state).
type PageView struct {
	ID               string
	Title            string
	MenuPath         []string
	MenuGroup        string
	Route            string
	URL              string
	DeepLinkTemplate string
	RequiresEntity   []string
	Description      string
	Capabilities     []string
	LookupHint       string
	Hidden           bool
	// MenuKey is the admin console's own menu permission code. It is NOT an
	// MCP permission and nothing here checks it; see README.md.
	MenuKey string
}

// Match is a PageView plus why it surfaced. Score and MatchedOn exist for
// auditing a ranking, not for showing to an operator.
type Match struct {
	PageView
	Score     int
	MatchedOn []string
}

// FindResult is the whole answer to one search, including the misses: an empty
// Matches with a populated KnownGaps is a successful, informative result.
type FindResult struct {
	Query      string
	Matches    []Match
	Total      int
	KnownGaps  []KnownGap
	MenuGroups []string
}
