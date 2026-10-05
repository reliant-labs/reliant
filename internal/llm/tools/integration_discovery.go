// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// =============================================================================
// INTEGRATION DISCOVERY TOOLS
// =============================================================================
// search_integrations and get_integration_schema are how an agent finds and
// reads integration actions (and, once they exist, trigger types) before
// writing a workflow's `type: action` node. They are backed by the same index
// as CatalogService.SearchCatalog / GetCatalogEntry, so the agent and the
// builder see one ranking. Neither ever prints the whole catalog: search
// returns a short ranked list, and a schema is fetched one ref at a time.

// CatalogSearcher is the caller-aware catalog index the discovery tools read.
type CatalogSearcher interface {
	Search(ctx context.Context, userID string, q catalogindex.Query) (*catalogindex.Result, error)
	Get(ctx context.Context, userID, ref string) (*catalogindex.Entry, bool, error)
}

const (
	searchIntegrationsDefaultLimit = 10
	searchIntegrationsMaxLimit     = 25
)

// catalogSearcher is the factory's searcher, or one over the embedded
// catalog with no connection state when none was injected (the daemon runtime
// and doc generators), so the tool still finds refs and schemas.
func (f *ToolsFactory) catalogSearcher() CatalogSearcher {
	if f != nil && f.opts != nil && f.opts.CatalogSearch != nil {
		return f.opts.CatalogSearch
	}
	idx, err := catalogindex.Builtin()
	if err != nil {
		return nil
	}
	return catalogindex.NewService(idx, nil, nil)
}

func (f *ToolsFactory) SearchIntegrations() Tool {
	return NewSearchIntegrationsTool(f.catalogSearcher())
}

func (f *ToolsFactory) GetIntegrationSchema() Tool {
	return NewGetIntegrationSchemaTool(f.catalogSearcher())
}

// -----------------------------------------------------------------------------
// search_integrations
// -----------------------------------------------------------------------------

type SearchIntegrationsParams struct {
	Query         string `json:"query" jsonschema:"required,description=What you want to do or which service (e.g. 'create github issue'\\, 'send slack message'\\, 'http request'). Words match names\\, keywords and descriptions."`
	Kind          string `json:"kind,omitempty" jsonschema:"enum=action,enum=trigger,description=Only actions (steps a workflow runs) or only triggers (events that start a run). Omit for both."`
	ConnectedOnly bool   `json:"connected_only,omitempty" jsonschema:"description=Only return what the user can use right now without connecting an account first."`
	Limit         int    `json:"limit,omitempty" jsonschema:"description=Maximum results (default 10\\, max 25)."`
}

type searchIntegrationsTool struct {
	search CatalogSearcher
}

const searchIntegrationsDescription = `Search the integration catalog for actions a workflow can run (and trigger types that can start one).

WHEN TO USE:
- Before writing a workflow node of ` + "`type: action`" + `: find the ref to put in its ` + "`uses:`" + `
- To check whether an integration (GitHub, Slack, HTTP, ...) can do something
- To see which integrations the user has already connected

RETURNS:
Up to ` + "`limit`" + ` refs, best match first, each with a one-line summary and whether the user can use it
now ("connected"). A "not connected" action still works in a workflow once the user connects that
integration; tell them so.

NEXT STEP:
Call get_integration_schema with a ref to read its parameters and output before writing the node:

    - id: open_issue
      type: action
      uses: github/issue.create@1      # the ref
      with: { ... }                    # keys from the params schema

Core workflow nodes (call_llm, run, loop, ...) are not integrations; use get_schema for those.`

func NewSearchIntegrationsTool(search CatalogSearcher) Tool {
	return NewToolWrapper[SearchIntegrationsParams, ToolResponse](&searchIntegrationsTool{search: search})
}

func (t *searchIntegrationsTool) Name() string        { return ToolSearchIntegrations }
func (t *searchIntegrationsTool) Description() string { return searchIntegrationsDescription }
func (t *searchIntegrationsTool) RequiresPermission(SearchIntegrationsParams) (bool, error) {
	return false, nil
}

func (t *searchIntegrationsTool) Execute(ctx *rctx.ToolContext, args SearchIntegrationsParams) (ToolResponse, error) {
	if t.search == nil {
		return NewTextErrorResponse("the integration catalog is not available here"), nil
	}
	limit := args.Limit
	if limit <= 0 {
		limit = searchIntegrationsDefaultLimit
	}
	if limit > searchIntegrationsMaxLimit {
		limit = searchIntegrationsMaxLimit
	}
	q := catalogindex.Query{Text: args.Query, ConnectedOnly: args.ConnectedOnly, PageSize: limit}
	switch strings.ToLower(strings.TrimSpace(args.Kind)) {
	case "":
	case "action":
		q.Kinds = []catalogindex.Kind{catalogindex.KindAction}
	case "trigger":
		q.Kinds = []catalogindex.Kind{catalogindex.KindTrigger}
	default:
		return NewTextErrorResponse(fmt.Sprintf("kind must be \"action\" or \"trigger\", not %q", args.Kind)), nil
	}
	userID, _ := auth.GetUserIDFromContext(ctx)
	res, err := t.search.Search(ctx, userID, q)
	if err != nil {
		if errors.Is(err, catalogindex.ErrQueryTooLong) {
			return NewTextErrorResponse(fmt.Sprintf("query is too long (max %d characters)", catalogindex.MaxQueryLen)), nil
		}
		return NewTextErrorResponse("integration search failed: " + err.Error()), nil
	}
	return NewTextResponse(renderIntegrationSearch(args.Query, res)), nil
}

func renderIntegrationSearch(query string, res *catalogindex.Result) string {
	var sb strings.Builder
	if len(res.Hits) == 0 {
		fmt.Fprintf(&sb, "No integration actions or triggers match %q.\n\n", query)
		sb.WriteString("Try fewer or broader words (a service name like \"github\", or a verb like \"send\"). ")
		sb.WriteString("For a service with no integration, the generic `http/request@1` action calls any public HTTP API.\n")
		return sb.String()
	}
	fmt.Fprintf(&sb, "# Integrations matching %q\n\n", query)
	fmt.Fprintf(&sb, "Showing %d of %d.\n\n", len(res.Hits), res.Total)
	sb.WriteString("| Ref | Kind | Summary | Connected |\n")
	sb.WriteString("|-----|------|---------|-----------|\n")
	for _, h := range res.Hits {
		e := h.Entry
		connected := "yes"
		if !h.Connected {
			connected = "no — the user must connect " + e.Manifest.GetDisplayName()
		} else if !e.ConnectionRequired {
			connected = "yes (no connection needed)"
		}
		fmt.Fprintf(&sb, "| `%s` | %s | %s | %s |\n", e.Ref, e.Kind, tableCell(e.Summary), connected)
	}
	sb.WriteString("\nCall get_integration_schema with a ref for its params and output schema.\n")
	return sb.String()
}

func tableCell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.ReplaceAll(s, "|", "\\|")
}

// -----------------------------------------------------------------------------
// get_integration_schema
// -----------------------------------------------------------------------------

type GetIntegrationSchemaParams struct {
	Ref string `json:"ref" jsonschema:"required,description=An integration ref from search_integrations\\, e.g. 'github/user.get@1' or 'http/request@1'."`
}

type getIntegrationSchemaTool struct {
	search CatalogSearcher
}

const getIntegrationSchemaDescription = `Get the full schema of one integration action (or trigger type) by ref.

WHEN TO USE:
- After search_integrations, before writing a ` + "`type: action`" + ` node that uses the ref
- To learn which keys go in the node's ` + "`with:`" + ` and which are required
- To learn the output fields reachable as ` + "`nodes.<id>.data.<field>`" + ` in later nodes

RETURNS:
The description, the params JSON Schema (the node's ` + "`with:`" + `), the output JSON Schema (` + "`nodes.<id>.data`" + `),
what connecting the integration requires, and whether the user is connected. A trigger type returns its
event payload schema (` + "`trigger.payload`" + `) instead of params and output.`

func NewGetIntegrationSchemaTool(search CatalogSearcher) Tool {
	return NewToolWrapper[GetIntegrationSchemaParams, ToolResponse](&getIntegrationSchemaTool{search: search})
}

func (t *getIntegrationSchemaTool) Name() string        { return ToolGetIntegrationSchema }
func (t *getIntegrationSchemaTool) Description() string { return getIntegrationSchemaDescription }
func (t *getIntegrationSchemaTool) RequiresPermission(GetIntegrationSchemaParams) (bool, error) {
	return false, nil
}

func (t *getIntegrationSchemaTool) Execute(ctx *rctx.ToolContext, args GetIntegrationSchemaParams) (ToolResponse, error) {
	if t.search == nil {
		return NewTextErrorResponse("the integration catalog is not available here"), nil
	}
	ref := strings.TrimSpace(args.Ref)
	if ref == "" {
		return NewTextErrorResponse("ref is required, e.g. \"http/request@1\"; find one with search_integrations"), nil
	}
	userID, _ := auth.GetUserIDFromContext(ctx)
	e, connected, err := t.search.Get(ctx, userID, ref)
	if err != nil {
		if errors.Is(err, catalogindex.ErrNotFound) {
			return NewTextErrorResponse(fmt.Sprintf("no integration action or trigger has ref %q. Refs look like <integration>/<action>@<major> "+
				"(e.g. http/request@1); find one with search_integrations", ref)), nil
		}
		return NewTextErrorResponse("reading the integration schema failed: " + err.Error()), nil
	}
	return NewTextResponse(renderIntegrationSchema(e, connected)), nil
}

func renderIntegrationSchema(e *catalogindex.Entry, connected bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s (`%s`)\n\n", e.DisplayName, e.Ref)
	fmt.Fprintf(&sb, "Integration: %s · Kind: %s", e.Manifest.GetDisplayName(), e.Kind)
	if e.Kind == catalogindex.KindAction && e.Action.GetMutates() {
		sb.WriteString(" · changes external state")
	}
	sb.WriteString("\n\n")
	if d := strings.TrimSpace(e.Description); d != "" {
		sb.WriteString(d + "\n\n")
	} else if e.Summary != "" {
		sb.WriteString(e.Summary + "\n\n")
	}

	sb.WriteString("## Connection\n\n")
	switch {
	case !e.ConnectionRequired && len(e.AuthKinds) == 0:
		sb.WriteString("None needed.\n\n")
	case !e.ConnectionRequired:
		fmt.Fprintf(&sb, "Optional. Without one the call is unauthenticated; with one (auth: %s) set the node's `connection:` to its id.\n\n",
			strings.Join(e.AuthKinds, ", "))
	case connected:
		fmt.Fprintf(&sb, "Required, and the user can use it now (auth: %s). Omit `connection:` to use their default %s connection.\n\n",
			strings.Join(e.AuthKinds, ", "), e.Manifest.GetDisplayName())
	default:
		fmt.Fprintf(&sb, "Required, and the user has NOT connected %s (auth: %s). The workflow can still be written; it will fail "+
			"at this step until they connect it in Settings.\n\n", e.Manifest.GetDisplayName(), strings.Join(e.AuthKinds, ", "))
	}

	if e.Kind == catalogindex.KindAction {
		params, output := e.Schemas()
		sb.WriteString("## Params (the node's `with:`)\n\n")
		writeJSONBlock(&sb, params)
		sb.WriteString("## Output (`nodes.<id>.data`)\n\n")
		writeJSONBlock(&sb, output)
		sb.WriteString("## Usage\n\n```yaml\n")
		fmt.Fprintf(&sb, "- id: %s\n  type: action\n  uses: %s\n  with:\n", nodeIDFor(e.ID), e.Ref)
		if names := requiredParams(params); len(names) > 0 {
			for _, n := range names {
				fmt.Fprintf(&sb, "    %s: ...\n", n)
			}
		} else {
			sb.WriteString("    {}\n")
		}
		sb.WriteString("```\n")
		if tool := e.ToolName(); tool != "" {
			fmt.Fprintf(&sb, "\nAlso available to agents as the `%s` tool.\n", tool)
		}
	}
	// SEAM (stream B): a trigger's payload schema (trigger.payload) renders
	// here once TriggerSpec carries one.
	return sb.String()
}

func writeJSONBlock(sb *strings.Builder, v map[string]any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		b = []byte("{}")
	}
	sb.WriteString("```json\n")
	sb.Write(b)
	sb.WriteString("\n```\n\n")
}

func requiredParams(schema map[string]any) []string {
	raw, _ := schema["required"].([]any)
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if s, ok := r.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func nodeIDFor(actionID string) string {
	return strings.NewReplacer(".", "_", "-", "_").Replace(actionID)
}
