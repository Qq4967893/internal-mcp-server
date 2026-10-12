package navigation

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const maxEntityParams = 4

var (
	// ErrPageNotFound is returned for an unknown page id. Callers surface it as
	// an INVALID_ARGUMENT envelope, not a transport error.
	ErrPageNotFound = errors.New("navigation: page not found")

	entityKeyPattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)
	entityValuePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// view builds the link-bearing form of a page. deep_link_template is always
// present; url only when nothing is left to substitute, which is 50 of the 54
// pages. The other four say so via RequiresEntity rather than handing back a
// link with a literal :roleId in it.
func (m *Manifest) view(page *Page) PageView {
	template := m.consoleBaseURL + page.Route
	absolute := ""
	if len(page.EntityParams) == 0 {
		absolute = template
	}
	return PageView{
		ID:               page.ID,
		Title:            page.Title,
		MenuPath:         append([]string(nil), page.MenuPath...),
		MenuGroup:        page.MenuGroup(),
		Route:            page.Route,
		URL:              absolute,
		DeepLinkTemplate: template,
		RequiresEntity:   append([]string(nil), page.EntityParams...),
		Description:      page.Description,
		Capabilities:     append([]string(nil), page.Capabilities...),
		LookupHint:       page.LookupHint,
		Hidden:           page.Hidden,
		MenuKey:          page.MenuKey,
	}
}

// Get returns one page by id, optionally resolving its dynamic route segments
// into an absolute link. The server does the substitution because §13.4 forbids
// letting the model assemble console URLs itself.
func (m *Manifest) Get(pageID string, entityParams map[string]string) (PageView, []KnownGap, error) {
	page, ok := m.byID[strings.TrimSpace(pageID)]
	if !ok {
		return PageView{}, nil, fmt.Errorf("%w: %q", ErrPageNotFound, pageID)
	}

	view := m.view(page)
	if len(entityParams) > 0 || len(page.EntityParams) > 0 {
		resolved, err := m.resolve(page, entityParams)
		if err != nil {
			return PageView{}, nil, err
		}
		view.URL = resolved
	}
	return view, m.gapsForPage(page), nil
}

// resolve substitutes :param segments. The charset restriction plus PathEscape
// is what stops "../", "%2f", "?" or "#" typed into a chat box from turning a
// console link into something else. An empty result means the caller supplied
// nothing and the page still needs values.
func (m *Manifest) resolve(page *Page, values map[string]string) (string, error) {
	if len(page.EntityParams) == 0 {
		if len(values) > 0 {
			return "", fmt.Errorf("navigation: page %q takes no entity params", page.ID)
		}
		return m.consoleBaseURL + page.Route, nil
	}
	if len(values) == 0 {
		// Not an error: the caller is allowed to look a page up before it has
		// an entity in hand. RequiresEntity tells it what is missing.
		return "", nil
	}
	if len(values) > maxEntityParams {
		return "", fmt.Errorf("navigation: at most %d entity params are accepted", maxEntityParams)
	}

	required := make(map[string]bool, len(page.EntityParams))
	for _, param := range page.EntityParams {
		required[param] = true
	}
	for key, value := range values {
		switch {
		case !entityKeyPattern.MatchString(key):
			return "", fmt.Errorf("navigation: entity param name %q is not a plain identifier", key)
		case !required[key]:
			return "", fmt.Errorf("navigation: page %q has no entity param %q; it takes %s", page.ID, key, strings.Join(page.EntityParams, ", "))
		case !entityValuePattern.MatchString(value):
			return "", fmt.Errorf("navigation: entity param %q value must be 1-64 characters of letters, digits, hyphen or underscore", key)
		}
	}
	for _, param := range page.EntityParams {
		if _, ok := values[param]; !ok {
			return "", fmt.Errorf("navigation: page %q needs entity param %q", page.ID, param)
		}
	}

	segments := strings.Split(page.Route, "/")
	for i, segment := range segments {
		if !strings.HasPrefix(segment, ":") {
			continue
		}
		segments[i] = url.PathEscape(values[segment[1:]])
	}
	resolved, err := url.Parse(m.consoleBaseURL + strings.Join(segments, "/"))
	if err != nil {
		return "", fmt.Errorf("navigation: page %q link: %w", page.ID, err)
	}
	return resolved.String(), nil
}
