// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// workflowCatalogReads is what a workflowCatalog is loaded from: the caller's
// workflows in one query and the project's synced workflows in one column.
type workflowCatalogReads interface {
	ListWorkflowDraftsByUser(ctx context.Context, userID string) ([]*db.WorkflowDraft, error)
	GetProjectWorkflowsJSON(ctx context.Context, projectID string) (*string, error)
}

// workflowCatalog is one request's snapshot of every workflow a ref can name
// for one user in one project: all of the user's own workflows and the
// project's synced ones. It answers workflowsource.Store from memory.
//
// A listing resolves every ref of every workflow it lists, twice: validating
// each of the user's workflows and working out what each needs a machine for.
// Resolving through the repository read the project's synced workflows and
// looked the slug up among the user's own on EVERY ref — 522 reads for a
// dozen workflows in TestListWorkflows_ReadsEachSourceOncePerListing, and in
// prod each project read was a full 18 MB config row. That is what put
// ListWorkflows at 5–10s p90. The user's workflows are already in hand (the
// listing reads them all), so the snapshot serves the slug lookups from them,
// with exactly db.Repo's GetUsableWorkflowBySlug / GetWorkflowDraftBySlug
// semantics.
type workflowCatalog struct {
	userID    string
	projectID string

	drafts []*db.WorkflowDraft
	bySlug map[string]*db.WorkflowDraft
	// draftsErr is a failure to read the user's workflows. Lookups report it
	// as a store failure rather than "you have none", which would let a
	// project workflow of the same name stand in for theirs.
	draftsErr error

	projectWorkflows *string
	projectErr       error
}

var _ workflowsource.Store = (*workflowCatalog)(nil)

// loadWorkflowCatalog reads the user's workflows and the project's synced
// workflows, concurrently. Read failures are kept, not returned: a listing
// still shows the sources that did load, and a resolution that needs a
// failed source reports the failure.
func loadWorkflowCatalog(ctx context.Context, reads workflowCatalogReads, userID, projectID string) *workflowCatalog {
	c := &workflowCatalog{userID: userID, projectID: projectID}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.drafts, c.draftsErr = reads.ListWorkflowDraftsByUser(ctx, userID)
	}()
	if projectID != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.projectWorkflows, c.projectErr = reads.GetProjectWorkflowsJSON(ctx, projectID)
		}()
	}
	wg.Wait()

	c.bySlug = make(map[string]*db.WorkflowDraft, len(c.drafts))
	for _, d := range c.drafts {
		c.bySlug[d.Slug] = d
	}
	return c
}

func (c *workflowCatalog) checkUser(userID string) error {
	if userID != c.userID {
		return fmt.Errorf("workflow catalog holds the workflows of one user, not %q", userID)
	}
	return c.draftsErr
}

// GetProjectWorkflowsJSON implements workflowsource.ProjectStore for the one
// project the catalog was loaded for. Any other project has nothing synced as
// far as this request can see.
func (c *workflowCatalog) GetProjectWorkflowsJSON(_ context.Context, projectID string) (*string, error) {
	if projectID != c.projectID || projectID == "" {
		return nil, sql.ErrNoRows
	}
	return c.projectWorkflows, c.projectErr
}

// GetWorkflowDraftBySlug is db.Repo's: the user's workflow with this slug,
// whatever its status, or nil.
func (c *workflowCatalog) GetWorkflowDraftBySlug(_ context.Context, userID, slug string) (*db.WorkflowDraft, error) {
	if err := c.checkUser(userID); err != nil {
		return nil, err
	}
	return c.bySlug[slug], nil
}

// GetUsableWorkflowBySlug is db.Repo's: a complete, visible workflow; a
// *db.WorkflowDraftNotRunnableError for a visible draft; otherwise nil.
func (c *workflowCatalog) GetUsableWorkflowBySlug(_ context.Context, userID, slug string) (*db.WorkflowDraft, error) {
	if err := c.checkUser(userID); err != nil {
		return nil, err
	}
	d := c.bySlug[slug]
	if d == nil || d.IsHidden {
		return nil, nil
	}
	if d.Status != db.WorkflowDraftStatusComplete {
		return nil, &db.WorkflowDraftNotRunnableError{Slug: slug}
	}
	return d, nil
}

// memoizeRefLoader resolves each distinct ref once. Listing walks the same
// refs over and over — most workflows ref builtin://agent — and every
// resolution re-parses the definition, which was the largest CPU cost of a
// listing once its reads were gone. Each caller gets its own copy of the
// workflow: validation compiles what it loads, and a shared message must not
// carry one caller's work into the next.
func memoizeRefLoader(load func(ref string) (*reliantv1.Workflow, error)) func(ref string) (*reliantv1.Workflow, error) {
	type result struct {
		wf  *reliantv1.Workflow
		err error
	}
	seen := map[string]result{}
	return func(ref string) (*reliantv1.Workflow, error) {
		key := workflowref.RefKey(ref)
		r, ok := seen[key]
		if !ok {
			r.wf, r.err = load(ref)
			seen[key] = r
		}
		if r.wf == nil {
			return nil, r.err
		}
		return proto.Clone(r.wf).(*reliantv1.Workflow), r.err
	}
}

// builtinParses holds each embedded builtin workflow parsed once per process.
// Builtins are compiled into the binary, yet every listing parsed all of them
// (~40ms of CPU) and re-parsed builtin://agent for every workflow that refs
// it. Keyed by "file:<name>" for a listed file and by a builtin ref's Key for
// a resolved ref; only successful parses are kept, so a user's ref to a
// builtin that does not exist cannot grow it.
var builtinParses sync.Map // string -> *reliantv1.Workflow

func cachedBuiltinParse(key string, parse func() (*reliantv1.Workflow, error)) (*reliantv1.Workflow, error) {
	if wf, ok := builtinParses.Load(key); ok {
		return proto.Clone(wf.(*reliantv1.Workflow)).(*reliantv1.Workflow), nil
	}
	wf, err := parse()
	if err != nil {
		return nil, err
	}
	builtinParses.Store(key, wf)
	return proto.Clone(wf).(*reliantv1.Workflow), nil
}

// parseBuiltinWorkflowFile is parseWorkflowYAML for an embedded builtin
// workflow file, from the process cache.
func parseBuiltinWorkflowFile(name string, data []byte) (*reliantv1.Workflow, error) {
	return cachedBuiltinParse("file:"+name, func() (*reliantv1.Workflow, error) {
		return parseWorkflowYAML(data)
	})
}

// withBuiltinCache answers builtin:// refs from the process cache, exactly as
// workflowref resolves them (no user or project workflow can shadow a
// builtin), and every other ref through load.
func withBuiltinCache(load func(ref string) (*reliantv1.Workflow, error)) func(ref string) (*reliantv1.Workflow, error) {
	return func(ref string) (*reliantv1.Workflow, error) {
		parsed, err := workflowref.Parse(ref)
		if err != nil || parsed.Kind != workflowref.Builtin {
			return load(ref)
		}
		return cachedBuiltinParse(parsed.Key(), func() (*reliantv1.Workflow, error) {
			resolved, err := workflowref.Resolve(ref, workflowref.Sources{})
			if err != nil {
				return nil, err
			}
			return resolved.Workflow, nil
		})
	}
}

// resolvingToSelf is workflowsource.DraftLoader's rule for the workflow being
// validated: a ref to itself resolves to the definition in hand, so a draft
// that spawns itself validates. Every other ref goes to load, which a listing
// shares across all the workflows it validates.
func resolvingToSelf(self *reliantv1.Workflow, load func(ref string) (*reliantv1.Workflow, error)) func(ref string) (*reliantv1.Workflow, error) {
	selfKey := ""
	if self != nil && self.GetName() != "" {
		selfKey = workflowref.RefKey(self.GetName())
	}
	return func(ref string) (*reliantv1.Workflow, error) {
		if selfKey != "" && workflowref.RefKey(ref) == selfKey {
			return self, nil
		}
		return load(ref)
	}
}

// memoizePreflight answers each distinct tool question once. Machine analysis
// asks the same ones of every workflow (the builtin agent's default tool
// filter appears in most of them), and each answer rebuilds the tool
// registry.
func memoizePreflight(cfg *v2.PreflightConfig) *v2.PreflightConfig {
	if cfg == nil {
		return nil
	}
	memo := *cfg
	if cfg.ExpandToolFilter != nil {
		expanded := map[string][]string{}
		memo.ExpandToolFilter = func(filter []string) []string {
			key := strings.Join(filter, "\x00")
			out, ok := expanded[key]
			if !ok {
				out = cfg.ExpandToolFilter(filter)
				expanded[key] = out
			}
			return append([]string(nil), out...)
		}
	}
	memo.IsDaemonTool = memoizeToolCheck(cfg.IsDaemonTool)
	memo.NeedsMachine = memoizeToolCheck(cfg.NeedsMachine)
	return &memo
}

func memoizeToolCheck(check v2.DaemonToolChecker) v2.DaemonToolChecker {
	if check == nil {
		return nil
	}
	answers := map[string]bool{}
	return func(name string) bool {
		answer, ok := answers[name]
		if !ok {
			answer = check(name)
			answers[name] = answer
		}
		return answer
	}
}
