package navigation

import (
	"errors"
	"slices"
	"strings"
)

// Scoring constants. These are tuned against the golden table in
// search_test.go, which is the thing that must be updated when they change --
// the numbers on their own mean nothing.
const (
	// An id is an exact handle, not a guess: it wins outright.
	scoreIDExact      = 1000
	scoreMenuKeyExact = 200

	scoreKeywordExact    = 100
	scoreKeywordContains = 60 // a keyword contains the whole query
	scorePhraseContains  = 45 // the query sentence contains a whole keyword
	scoreMenuPathExact   = 40
	scoreMenuPathPartial = 25
	scoreCapability      = 25
	scoreDescription     = 20
	// Endpoint names are unique identifiers, so an exact one is as precise a
	// signal as an exact keyword; a partial one is barely a signal at all.
	scoreAPIEndpointExact = 45
	scoreAPIEndpoint      = 15
	scoreLookupHint       = 10

	// Gram coverage is capped so a long query cannot bigram-carpet the whole
	// manifest and let a prose-heavy page out-score an exact keyword hit.
	// Four keyword-gram hits (24) reach the threshold exactly; one real
	// substring hit (45) always beats pure gram noise.
	scoreGramKeyword    = 6
	scoreGramKeywordCap = 48
	scoreGramBody       = 2
	scoreGramBodyCap    = 16

	// minScore is what stops a single-character query from returning a third
	// of the manifest.
	minScore = 24

	// Hidden pages are demoted, never dropped: they include the only four
	// deep-linkable routes, so excluding them would make 修改角色 unreachable.
	// Three fifths is enough to keep /roles/settings ahead of
	// /roles/edit/:roleId for the query 角色.
	hiddenNumerator   = 3
	hiddenDenominator = 5

	maxQueryRunes = 80
	maxGrams      = 48

	// DefaultLimit keeps a result set small enough to stay readable in a model
	// context: each match carries a description, capabilities and a hint.
	DefaultLimit = 5
	MaxLimit     = 10
	maxKnownGaps = 2
)

var (
	// ErrEmptyQuery and ErrQueryTooLong are surfaced as INVALID_ARGUMENT.
	ErrEmptyQuery   = errors.New("navigation: query must not be empty unless a menu group is given")
	ErrQueryTooLong = errors.New("navigation: query must be at most 80 characters")

	// ErrUnknownMenuGroup names the valid groups, because a caller that
	// guessed once will otherwise guess again.
	ErrUnknownMenuGroup = errors.New("navigation: unknown menu group")
)

// FindRequest is one search. An empty Query is allowed only with a MenuGroup,
// which turns the call into "list that menu".
type FindRequest struct {
	Query     string
	MenuGroup string
	Limit     int
}

// query is a normalised search string broken into the forms the scorer needs.
// There is no word segmentation here: Chinese has no spaces and this package
// has no tokenizer, so CJK runs become character bigrams instead.
type query struct {
	phrase string
	grams  []string
}

func parseQuery(raw string) (query, error) {
	if runeLen(strings.TrimSpace(raw)) > maxQueryRunes {
		return query{}, ErrQueryTooLong
	}
	phrase := normalize(raw)
	if phrase == "" {
		return query{}, ErrEmptyQuery
	}
	return query{phrase: phrase, grams: buildGrams(strings.Fields(phrase))}, nil
}

func buildGrams(segments []string) []string {
	seen := make(map[string]bool)
	grams := make([]string, 0, maxGrams)
	add := func(gram string) {
		if gram == "" || seen[gram] || len(grams) >= maxGrams {
			return
		}
		seen[gram] = true
		grams = append(grams, gram)
	}
	for _, segment := range segments {
		runes := []rune(segment)
		if !hasIdeographic(segment) || len(runes) == 1 {
			add(segment)
			continue
		}
		for i := 0; i+2 <= len(runes); i++ {
			add(string(runes[i : i+2]))
		}
		if len(runes) >= 3 {
			add(segment)
		}
	}
	return grams
}

// Find runs the deterministic search described in technical design §12.6:
// id lookup, then keyword matching, then ranking, then menu-group filtering.
// No vector search -- with 54 pages the deterministic path is both better and
// auditable.
func (m *Manifest) Find(request FindRequest) (FindResult, error) {
	group := strings.TrimSpace(request.MenuGroup)
	if group != "" && !slices.Contains(m.menuGroups, group) {
		return FindResult{}, ErrUnknownMenuGroup
	}

	limit := request.Limit
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}

	result := FindResult{Query: request.Query, MenuGroups: m.MenuGroups()}

	if strings.TrimSpace(request.Query) == "" {
		if group == "" {
			return FindResult{}, ErrEmptyQuery
		}
		return m.browse(group, limit, result), nil
	}

	parsed, err := parseQuery(request.Query)
	if err != nil {
		return FindResult{}, err
	}

	matches := make([]Match, 0, len(m.pages))
	for i := range m.pages {
		page := &m.pages[i]
		if group != "" && page.MenuGroup() != group {
			continue
		}
		score, reasons := scorePage(page, parsed)
		if score < minScore {
			continue
		}
		matches = append(matches, Match{PageView: m.view(page), Score: score, MatchedOn: reasons})
		if score == scoreIDExact {
			// An exact id is unambiguous; anything else would be noise.
			return finish(result, []Match{matches[len(matches)-1]}, 1, m.matchGaps(parsed)), nil
		}
	}

	slices.SortStableFunc(matches, func(a, b Match) int {
		return compareMatches(m.byID[a.ID], m.byID[b.ID], a, b)
	})

	total := len(matches)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return finish(result, matches, total, m.matchGaps(parsed)), nil
}

func finish(result FindResult, matches []Match, total int, gaps []KnownGap) FindResult {
	result.Matches = matches
	result.Total = total
	result.KnownGaps = gaps
	return result
}

// browse lists a whole menu group in manifest order, for "资金管理下面有哪些页面".
func (m *Manifest) browse(group string, limit int, result FindResult) FindResult {
	matches := make([]Match, 0, limit)
	total := 0
	for i := range m.pages {
		page := &m.pages[i]
		if page.MenuGroup() != group {
			continue
		}
		total++
		if len(matches) < limit {
			matches = append(matches, Match{PageView: m.view(page), MatchedOn: []string{"menu_group"}})
		}
	}
	return finish(result, matches, total, nil)
}

// compareMatches is a total order so ranking is reproducible in tests: score,
// then visible before hidden, then reviewed before undescribed, then manifest
// position.
func compareMatches(pageA, pageB *Page, a, b Match) int {
	if a.Score != b.Score {
		return b.Score - a.Score
	}
	if a.Hidden != b.Hidden {
		if a.Hidden {
			return 1
		}
		return -1
	}
	if pageA.Reviewed != pageB.Reviewed {
		if pageA.Reviewed {
			return -1
		}
		return 1
	}
	return pageA.index - pageB.index
}

func scorePage(page *Page, q query) (int, []string) {
	terms := &page.terms
	if q.phrase == terms.id {
		return scoreIDExact, []string{"id"}
	}

	score := 0
	reasons := make([]string, 0, 5)
	phraseRunes := runeLen(q.phrase)

	if terms.menuKey != "" && q.phrase == terms.menuKey {
		score += scoreMenuKeyExact
		reasons = append(reasons, "menu_key")
	}

	// max over keywords, not sum. The distribution is lopsided -- 客户列表 has
	// 12 keywords, 文章管理 has 1 -- so summing would reward the
	// well-documented page regardless of relevance. Let the best signal decide.
	bestKeywordScore, bestKeyword := 0, ""
	for _, keyword := range terms.keywords {
		candidate := 0
		switch {
		case keyword == q.phrase:
			candidate = scoreKeywordExact
		case phraseRunes >= 2 && strings.Contains(keyword, q.phrase):
			candidate = scoreKeywordContains
		case runeLen(keyword) >= 2 && strings.Contains(q.phrase, keyword):
			candidate = scorePhraseContains
		}
		if candidate > bestKeywordScore {
			bestKeywordScore, bestKeyword = candidate, keyword
		}
	}
	if bestKeywordScore > 0 {
		score += bestKeywordScore
		reasons = append(reasons, "keyword:"+bestKeyword)
	}

	bestMenuScore := 0
	for _, segment := range terms.menuPath {
		switch {
		case segment == q.phrase:
			bestMenuScore = max(bestMenuScore, scoreMenuPathExact)
		case phraseRunes >= 2 && strings.Contains(segment, q.phrase):
			bestMenuScore = max(bestMenuScore, scoreMenuPathPartial)
		}
	}
	if bestMenuScore > 0 {
		score += bestMenuScore
		reasons = append(reasons, "menu_path")
	}

	if phraseRunes >= 2 {
		if containsAny(terms.capabilities, q.phrase) {
			score += scoreCapability
			reasons = append(reasons, "capability")
		}
		if terms.description != "" && strings.Contains(terms.description, q.phrase) {
			score += scoreDescription
			reasons = append(reasons, "description")
		}
		bestAPIScore := 0
		for _, endpoint := range terms.apiEndpoints {
			switch {
			case endpoint == q.phrase:
				bestAPIScore = max(bestAPIScore, scoreAPIEndpointExact)
			case strings.Contains(endpoint, q.phrase):
				bestAPIScore = max(bestAPIScore, scoreAPIEndpoint)
			}
		}
		if bestAPIScore > 0 {
			score += bestAPIScore
			reasons = append(reasons, "api_endpoint")
		}
		if terms.lookupHint != "" && strings.Contains(terms.lookupHint, q.phrase) {
			score += scoreLookupHint
			reasons = append(reasons, "lookup_hint")
		}
	}

	keywordHits, bodyHits := 0, 0
	for _, gram := range q.grams {
		if strings.Contains(terms.keywordText, gram) {
			keywordHits++
		}
		if strings.Contains(terms.bodyText, gram) {
			bodyHits++
		}
	}
	if gramScore := min(scoreGramKeyword*keywordHits, scoreGramKeywordCap) +
		min(scoreGramBody*bodyHits, scoreGramBodyCap); gramScore > 0 {
		score += gramScore
		reasons = append(reasons, "grams")
	}

	if page.Hidden {
		score = score * hiddenNumerator / hiddenDenominator
	}
	return score, reasons
}

// matchGaps finds documented absences relevant to this query. Gaps are returned
// even when there are matches: "怎么给客户发公告" should surface 消息管理 and
// the fact that there is no dedicated announcements page.
//
// Matching is exact against hand-written keywords rather than heuristic against
// the topic, because a four-character topic like 公告管理 either misses the real
// question or fires on every sentence containing 管理.
func (m *Manifest) matchGaps(q query) []KnownGap {
	gaps := make([]KnownGap, 0, maxKnownGaps)
	for i := range m.knownGaps {
		gap := &m.knownGaps[i]
		if !gapMatches(gap, q) {
			continue
		}
		gaps = append(gaps, KnownGap{Topic: gap.Topic, Note: gap.Note, Keywords: gap.Keywords})
		if len(gaps) == maxKnownGaps {
			break
		}
	}
	if len(gaps) == 0 {
		return nil
	}
	return gaps
}

// gapMatches is deliberately one-directional: the query must mention a gap
// keyword. Matching the other way (a keyword containing the query) fires the
// 权限过滤 gap on any question about 权限 and the deep-link gap on any question
// about 记录, which is noise dressed up as insight.
func gapMatches(gap *KnownGap, q query) bool {
	if q.phrase == gap.terms.topic {
		return true
	}
	for _, keyword := range gap.terms.keywords {
		if runeLen(keyword) >= 2 && strings.Contains(q.phrase, keyword) {
			return true
		}
	}
	return false
}

// gapsForPage surfaces documented absences that bear on a single page, so a
// caller asking for the role-edit page also learns that record-level deep links
// barely exist in this console.
func (m *Manifest) gapsForPage(page *Page) []KnownGap {
	parsed := query{
		phrase: page.terms.keywordText,
		grams:  nil,
	}
	return m.matchGaps(parsed)
}
