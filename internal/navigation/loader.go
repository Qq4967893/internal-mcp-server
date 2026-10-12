package navigation

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Manifest is the loaded, validated navigation data. It is immutable after
// Load and safe for concurrent use.
type Manifest struct {
	pages          []Page
	byID           map[string]*Page
	knownGaps      []KnownGap
	menuGroups     []string
	consoleBaseURL string
	loadedAt       time.Time
}

type document struct {
	KnownGaps []KnownGap `yaml:"known_gaps"`
	Pages     []Page     `yaml:"pages"`
}

// Load parses the embedded manifest and rejects anything that cannot be turned
// into a safe console link. It is called once at startup and a failure must
// stop the process: a malformed manifest means every navigation answer would be
// wrong, and wrong links are worse than no tool.
func Load(data []byte, consoleBaseURL string) (*Manifest, error) {
	base := strings.TrimRight(strings.TrimSpace(consoleBaseURL), "/")
	if base == "" {
		return nil, errors.New("navigation: console base URL is required")
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("navigation: console base URL: %w", err)
	}

	var doc document
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	// Mirrors the config loader: an unrecognised key means the generator and
	// this reader have drifted, which should fail loudly rather than silently
	// drop a field.
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("navigation: decode manifest: %w", err)
	}
	if len(doc.Pages) == 0 {
		return nil, errors.New("navigation: manifest has no pages")
	}

	manifest := &Manifest{
		pages:          doc.Pages,
		byID:           make(map[string]*Page, len(doc.Pages)),
		knownGaps:      doc.KnownGaps,
		consoleBaseURL: base,
		loadedAt:       time.Now().UTC(),
	}

	var errs []error
	seenGroups := make(map[string]bool)
	for i := range manifest.pages {
		page := &manifest.pages[i]
		page.index = i
		if err := validatePage(page); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, duplicate := manifest.byID[page.ID]; duplicate {
			errs = append(errs, fmt.Errorf("navigation: duplicate page id %q", page.ID))
			continue
		}
		manifest.byID[page.ID] = page
		page.terms = buildPageTerms(page)
		if group := page.MenuGroup(); !seenGroups[group] {
			seenGroups[group] = true
			manifest.menuGroups = append(manifest.menuGroups, group)
		}
	}
	for i := range manifest.knownGaps {
		gap := &manifest.knownGaps[i]
		if strings.TrimSpace(gap.Topic) == "" {
			errs = append(errs, errors.New("navigation: known_gaps entry has an empty topic"))
			continue
		}
		gap.terms = gapTerms{
			topic:    normalize(gap.Topic),
			keywords: normalizeAll(gap.Keywords),
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return manifest, nil
}

// validatePage is the anti-open-redirect check on trusted-but-generated data.
// Everything here would otherwise end up concatenated onto the console base URL
// and handed to an operator as a link.
func validatePage(page *Page) error {
	var errs []error
	if strings.TrimSpace(page.ID) == "" {
		errs = append(errs, errors.New("navigation: page has an empty id"))
	}
	switch {
	case !strings.HasPrefix(page.Route, "/"):
		errs = append(errs, fmt.Errorf("navigation: page %q route %q must start with /", page.ID, page.Route))
	case strings.Contains(page.Route, "://"),
		strings.Contains(page.Route, ".."),
		strings.ContainsAny(page.Route, "#? \t\r\n"):
		errs = append(errs, fmt.Errorf("navigation: page %q route %q is not a plain path", page.ID, page.Route))
	}
	if len(page.MenuPath) == 0 {
		errs = append(errs, fmt.Errorf("navigation: page %q has an empty menu_path", page.ID))
	}
	for _, param := range page.EntityParams {
		if !routeHasParam(page.Route, param) {
			errs = append(errs, fmt.Errorf("navigation: page %q declares entity param %q that its route does not contain", page.ID, param))
		}
	}
	return errors.Join(errs...)
}

func routeHasParam(route, param string) bool {
	for _, segment := range strings.Split(route, "/") {
		if strings.HasPrefix(segment, ":") && segment[1:] == param {
			return true
		}
	}
	return false
}

func buildPageTerms(page *Page) pageTerms {
	keywords := append([]string{page.Title}, page.Keywords...)
	terms := pageTerms{
		id:           normalize(page.ID),
		menuKey:      normalize(page.MenuKey),
		keywords:     normalizeAll(keywords),
		menuPath:     normalizeAll(page.MenuPath),
		capabilities: normalizeAll(page.Capabilities),
		description:  normalize(page.Description),
		lookupHint:   normalize(page.LookupHint),
		apiEndpoints: normalizeAll(page.APIEndpoints),
	}
	terms.keywordText = strings.Join(terms.keywords, " ")
	terms.bodyText = strings.Join(append(
		append([]string{terms.description}, terms.capabilities...),
		terms.lookupHint,
	), " ")
	return terms
}

// Pages exposes the manifest in its generated order, for tests and for browsing
// a whole menu group.
func (m *Manifest) Pages() []Page { return m.pages }

// MenuGroups lists every top-level menu, in manifest order.
func (m *Manifest) MenuGroups() []string { return append([]string(nil), m.menuGroups...) }

// KnownGaps lists everything the admin console does not have.
func (m *Manifest) KnownGaps() []KnownGap { return append([]KnownGap(nil), m.knownGaps...) }

// LoadedAt is when this data entered the process. The generator deliberately
// writes no timestamp, so this is the most honest observed_at available -- and
// claiming per-call freshness for a file fixed at build time would overstate it.
func (m *Manifest) LoadedAt() time.Time { return m.loadedAt }
