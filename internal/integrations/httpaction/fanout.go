package httpaction

import (
	"context"
	"fmt"
	"slices"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/integrations/tmpl"
)

// fanOut runs a walk (see FanOutSpec): the parent request lists the parents,
// and fo.Request runs once for each. The action's output is every parent's
// items, in order.
func (r *Runner) fanOut(ctx context.Context, env callEnv, req *reliantv1.HttpRequestSpec, fo *reliantv1.FanOutSpec, params map[string]any, connVars map[string]any) (*Result, error) {
	listParents := func(p *page) (any, error) {
		vars := responseVars(p)
		vars["params"] = params
		vars["connection"] = connVars
		v, err := tmpl.EvalExpr(fo.GetItems(), vars)
		if err != nil {
			return nil, fmt.Errorf("fan_out.items: %w", err)
		}
		return v, nil
	}
	parents, err := r.request(ctx, env, req, map[string]any{"params": params, "connection": connVars}, listParents)
	if err != nil {
		return nil, err
	}
	if parents.result != nil {
		return parents.result, nil
	}
	limit := int(fo.GetMaxItems())
	if limit == 0 {
		limit = manifest.DefaultFanOutItems
	}
	if len(parents.items) > limit {
		return nil, fmt.Errorf("fan_out: the request listed %d items and at most %d are walked; narrow the request with params", len(parents.items), limit)
	}

	items := []any{}
	truncated := parents.truncated
	last := parents.last
	for _, parent := range parents.items {
		vars := map[string]any{"params": params, "connection": connVars, "parent": parent}
		selectPage := func(p *page) (any, error) { return selectOutputFor(env.a.GetOutput(), p, params, parent) }
		run, err := r.request(ctx, env, fo.GetRequest(), vars, selectPage)
		if err != nil {
			return nil, err
		}
		last = run.last
		if res := run.result; res != nil {
			if !res.Retryable && slices.Contains(fo.GetSkipStatus(), int32(res.StatusCode)) {
				continue
			}
			return res, nil
		}
		items = append(items, run.items...)
		truncated = truncated || run.truncated
	}
	res := success(last, map[string]any{manifest.ItemsOutputField: items})
	res.Truncated = truncated
	return res, nil
}
