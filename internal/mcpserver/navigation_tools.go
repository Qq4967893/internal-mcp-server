package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Global-MongoX/internal-mcp-server/internal/navigation"
	"github.com/Global-MongoX/internal-mcp-server/internal/registry"
	"github.com/Global-MongoX/internal-mcp-server/internal/toolruntime"
)

const navigationPermission = "admin.console.navigation.read"

// accessNote is attached to every page. The manifest carries menu_key, the
// admin console's own menu position code, which is a different vocabulary from
// MCP permissions and cannot be mapped onto them. Calling it
// required_permissions -- as technical design §13.3 originally did -- would be
// a lie the model repeats to operators, so it ships as access with checked
// false and this note.
const accessNote = "能否打开取决于该运营人员在后台的菜单权限；本工具不做权限过滤。"

// maxNavigationLinks caps the renderable links in one result. Three is enough
// to offer an alternative without turning an answer into a link dump.
const maxNavigationLinks = 3

type FindPageInput struct {
	Query     string  `json:"query" jsonschema:"What the operator wants to do, in their own words. Chinese, English or Japanese, at most 80 characters. Examples: 入金记录, where are withdrawals reviewed, ブラックリスト. May be empty only when menu_group is set."`
	MenuGroup *string `json:"menu_group,omitempty" jsonschema:"Optional top-level menu to search within. Leave unset unless the operator named a menu."`
	Limit     *int    `json:"limit,omitempty" jsonschema:"Optional number of pages to return, 1 to 10, default 5"`
}

type GetPageInput struct {
	PageID       string            `json:"page_id" jsonschema:"Exact page id from admin_console_find_page, for example finance.cash.deposit"`
	EntityParams map[string]string `json:"entity_params,omitempty" jsonschema:"Values for the page's dynamic route segments, keyed by the names listed in requires_entity, for example {\"roleId\": \"12\"}. Only four pages take these. Values may contain letters, digits, hyphen and underscore only."`
}

// PageAccess is the honest form of §13.3's required_permissions.
type PageAccess struct {
	MenuKey string `json:"menu_key" jsonschema:"The admin console's own menu permission code. This is NOT an MCP permission and was NOT checked."`
	Checked bool   `json:"checked" jsonschema:"Always false. This server cannot see the operator's console menu permissions."`
	Note    string `json:"note"`
}

type PageResult struct {
	ID               string     `json:"id" jsonschema:"Stable page id. Pass this to admin_console_get_page."`
	Title            string     `json:"title"`
	MenuPath         []string   `json:"menu_path" jsonschema:"Menu breadcrumb from the top-level group down to this page. Tell the operator this, not just the link."`
	MenuGroup        string     `json:"menu_group"`
	Route            string     `json:"route" jsonschema:"Path relative to the console. Never assemble a URL from this; use url or deep_link_template."`
	URL              string     `json:"url" jsonschema:"Absolute link built by this server. Empty when the page needs entity values; see requires_entity."`
	DeepLinkTemplate string     `json:"deep_link_template" jsonschema:"Absolute link with any :param segments left unresolved. Do not hand this to an operator with the placeholders still in it."`
	RequiresEntity   []string   `json:"requires_entity" jsonschema:"Dynamic route segment names that must be supplied to admin_console_get_page before a link exists. Only four pages in the whole console have any."`
	Description      string     `json:"description" jsonschema:"May be empty. If it is, say what the page is called and where it sits, and do not invent what it does."`
	Capabilities     []string   `json:"capabilities" jsonschema:"Concrete actions available on the page. May be empty."`
	LookupHint       string     `json:"lookup_hint" jsonschema:"How to find one specific record once the page is open. May be empty."`
	Hidden           bool       `json:"hidden" jsonschema:"true means the page is not in the sidebar and is reached from its list page."`
	Access           PageAccess `json:"access"`
	MatchScore       int        `json:"match_score,omitempty"`
	MatchedOn        []string   `json:"matched_on,omitempty" jsonschema:"Why this page matched, for auditing. Do not repeat this to the operator."`
}

type KnownGap struct {
	Topic string `json:"topic"`
	Note  string `json:"note"`
}

// NavigationLink is the cross-tool convention for a renderable deep link: any
// tool that can produce one puts it under a top-level navigation key, so the
// Agent layer stays free of per-tool branching.
type NavigationLink struct {
	Label       string `json:"label"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

type FindPageData struct {
	Query      string           `json:"query"`
	Matches    []PageResult     `json:"matches" jsonschema:"Ranked pages. Empty means no page matched; check known_gaps before saying you could not find anything."`
	KnownGaps  []KnownGap       `json:"known_gaps" jsonschema:"Documented absences relevant to this question. A non-empty entry means the admin console genuinely has no such page, which is a real answer."`
	MenuGroups []string         `json:"menu_groups" jsonschema:"Every top-level menu in the console, so a failed search can still suggest a direction."`
	Navigation []NavigationLink `json:"navigation" jsonschema:"Renderable links for the top matches."`
}

type GetPageData struct {
	Page       PageResult       `json:"page"`
	KnownGaps  []KnownGap       `json:"known_gaps"`
	Navigation []NavigationLink `json:"navigation"`
}

func registerNavigationTools(server *mcp.Server, deps Deps) {
	registerFindPageTool(server, deps)
	registerGetPageTool(server, deps)
}

// registerNavigationTool is the un-helpered form, because registerFinancialTool
// stamps every tool confidential_financial. A static menu manifest is not
// financial data and mislabelling it pollutes the sensitivity dimension of the
// audit log.
func registerNavigationTool[In, Out any](
	server *mcp.Server,
	deps Deps,
	definition registry.Definition,
	handler mcp.ToolHandlerFor[In, toolruntime.Envelope[Out]],
) {
	definition.Version = "1.0.0"
	definition.Owner = "ai-copilot-platform"
	definition.RequiredPermissions = []string{navigationPermission}
	definition.SensitivityLevel = "public_internal"
	definition.Timeout = deps.Timeout.String()
	definition.AuditPolicy = "metadata_only"
	if err := deps.Registry.Register(definition); err != nil {
		panic(err)
	}

	// No OutputSchema override: none of these structs carry json.Number, so
	// the SDK infers Envelope[Out] correctly on its own.
	mcp.AddTool(server, &mcp.Tool{
		Name:        definition.Name,
		Title:       definition.Title,
		Description: definition.Description,
	}, handler)
}

func registerFindPageTool(server *mcp.Server, deps Deps) {
	handler := func(_ context.Context, _ *mcp.CallToolRequest, input FindPageInput) (*mcp.CallToolResult, toolruntime.Envelope[FindPageData], error) {
		request := navigation.FindRequest{Query: input.Query}
		if input.MenuGroup != nil {
			request.MenuGroup = *input.MenuGroup
		}
		if input.Limit != nil {
			request.Limit = *input.Limit
		}

		result, err := deps.Navigation.Find(request)
		if err != nil {
			return nil, navigationFailure[FindPageData](err), nil
		}

		matches := make([]PageResult, 0, len(result.Matches))
		for _, match := range result.Matches {
			matches = append(matches, pageResult(match.PageView, match.Score, match.MatchedOn))
		}
		envelope := toolruntime.Success(FindPageData{
			Query:      result.Query,
			Matches:    matches,
			KnownGaps:  knownGaps(result.KnownGaps),
			MenuGroups: result.MenuGroups,
			Navigation: navigationLinks(matches),
		}, navigationSource(deps))
		if result.Total > len(matches) {
			envelope.Pagination = &toolruntime.Pagination{
				Limit:  len(matches),
				Offset: 0,
				Total:  result.Total,
			}
		}
		return nil, envelope, nil
	}

	registerNavigationTool(server, deps, definition(
		"admin_console_find_page",
		"Admin Console Page Finder",
		findPageDescription(deps),
	), handler)
}

func registerGetPageTool(server *mcp.Server, deps Deps) {
	handler := func(_ context.Context, _ *mcp.CallToolRequest, input GetPageInput) (*mcp.CallToolResult, toolruntime.Envelope[GetPageData], error) {
		view, gaps, err := deps.Navigation.Get(input.PageID, input.EntityParams)
		if err != nil {
			return nil, navigationFailure[GetPageData](err), nil
		}
		page := pageResult(view, 0, nil)
		return nil, toolruntime.Success(GetPageData{
			Page:       page,
			KnownGaps:  knownGaps(gaps),
			Navigation: navigationLinks([]PageResult{page}),
		}, navigationSource(deps)), nil
	}

	registerNavigationTool(server, deps, definition(
		"admin_console_get_page",
		"Admin Console Page Detail",
		"Returns one admin console page by its id, and builds the absolute link. "+
			"Call this after admin_console_find_page when a page listed requires_entity: pass the entity values as entity_params and this server substitutes them into the route. "+
			"The server builds the link; never assemble a console URL yourself.",
	), handler)
}

// findPageDescription is built from the loaded manifest so the documented menu
// groups cannot drift from the data. Technical design §9.3: the description is
// the Agent's only input for deciding when to reach for this tool.
func findPageDescription(deps Deps) string {
	return "Finds which admin console page does something, for an operator who knows what they want but not where it lives. " +
		"Returns the menu breadcrumb, an absolute link, what the page can do, and how to find a single record once it is open. " +
		"This is a lookup over the console's own menu and route table; it reads no business data and cannot tell you what is on a page. " +
		"An empty matches list with a populated known_gaps means the console genuinely has no such feature -- say so rather than 'not found'. " +
		"Valid menu_group values: " + strings.Join(deps.Navigation.MenuGroups(), ", ") + "."
}

func pageResult(view navigation.PageView, score int, matchedOn []string) PageResult {
	return PageResult{
		ID:               view.ID,
		Title:            view.Title,
		MenuPath:         view.MenuPath,
		MenuGroup:        view.MenuGroup,
		Route:            view.Route,
		URL:              view.URL,
		DeepLinkTemplate: view.DeepLinkTemplate,
		RequiresEntity:   view.RequiresEntity,
		Description:      view.Description,
		Capabilities:     view.Capabilities,
		LookupHint:       view.LookupHint,
		Hidden:           view.Hidden,
		Access:           PageAccess{MenuKey: view.MenuKey, Checked: false, Note: accessNote},
		MatchScore:       score,
		MatchedOn:        matchedOn,
	}
}

func knownGaps(gaps []navigation.KnownGap) []KnownGap {
	result := make([]KnownGap, 0, len(gaps))
	for _, gap := range gaps {
		// Keywords are a matching aid, not an answer; they stay internal.
		result = append(result, KnownGap{Topic: gap.Topic, Note: gap.Note})
	}
	return result
}

// navigationLinks keeps only pages that already have a resolved URL. A page
// still waiting on entity values has nothing renderable, and emitting the
// template with a literal :roleId in it would invite the model to fill it in.
func navigationLinks(pages []PageResult) []NavigationLink {
	links := make([]NavigationLink, 0, maxNavigationLinks)
	for _, page := range pages {
		if page.URL == "" || len(links) == maxNavigationLinks {
			continue
		}
		description := page.Description
		if description == "" {
			description = page.LookupHint
		}
		label := strings.Join(page.MenuPath, " / ")
		if label == "" {
			label = page.Title
		}
		links = append(links, NavigationLink{Label: label, URL: page.URL, Description: description})
	}
	return links
}

func navigationSource(deps Deps) toolruntime.Source {
	return toolruntime.Source{
		Service:  "internal-mcp-server",
		Resource: "knowledge/navigation/pages",
		// The manifest is static and the generator writes no timestamp, so
		// load time is the most honest freshness claim available. time.Now()
		// would imply the data was just fetched.
		ObservedAt: deps.Navigation.LoadedAt(),
	}
}

// navigationFailure maps every navigation error to INVALID_ARGUMENT: they are
// all caused by the call, never by an upstream, so none of them is retryable.
func navigationFailure[T any](err error) toolruntime.Envelope[T] {
	message := err.Error()
	if errors.Is(err, navigation.ErrPageNotFound) {
		message = fmt.Sprintf("%s. Use admin_console_find_page to get a valid page id.", message)
	}
	return toolruntime.Failure[T]("INVALID_ARGUMENT", message, false)
}
