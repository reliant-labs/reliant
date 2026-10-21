package inputoptions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/integrations/tmpl"
)

// evaluator is one compiled expression.
type evaluator = func(vars map[string]any) (any, error)

// projector is a type's CEL, compiled once. The manifest is immutable, so it
// is memoised per type.
type projector struct {
	items       evaluator
	value       evaluator
	label       evaluator
	description evaluator
	group       evaluator
	annotations map[string]evaluator
	warnings    []projectedWarning

	resolveLabel       evaluator
	resolveDescription evaluator
}

type projectedWarning struct {
	when    evaluator
	message string
}

func (s *service) projectorFor(rt *resolvedType) (*projector, error) {
	if p, ok := s.projectors.Load(rt.ref); ok {
		return p.(*projector), nil
	}
	p, err := buildProjector(rt)
	if err != nil {
		return nil, &detailError{ErrInvalidType, fmt.Sprintf("%s: %v", rt.ref, err)}
	}
	s.projectors.Store(rt.ref, p)
	return p, nil
}

func buildProjector(rt *resolvedType) (*projector, error) {
	o := rt.o
	p := &projector{annotations: map[string]evaluator{}}
	var err error
	prep := func(what, expr string, names ...string) evaluator {
		if err != nil || expr == "" {
			return nil
		}
		var e evaluator
		e, err = tmpl.Prepare(expr, names...)
		if err != nil {
			err = fmt.Errorf("%s: %w", what, err)
		}
		return e
	}
	itemsExpr := o.GetItems()
	if itemsExpr == "" {
		itemsExpr = manifest.VarOutput + "." + manifest.ItemsOutputField
	}
	p.items = prep("items", itemsExpr, manifest.VarOutput)
	p.value = prep("value", o.GetValue(), manifest.VarItem)
	p.label = prep("label", o.GetLabel(), manifest.VarItem)
	p.description = prep("description", o.GetDescription(), manifest.VarItem)
	p.group = prep("group", o.GetGroup(), manifest.VarItem)
	for name, expr := range o.GetAnnotations() {
		p.annotations[name] = prep("annotations."+name, expr, manifest.VarItem)
	}
	for i, w := range o.GetWarnings() {
		p.warnings = append(p.warnings, projectedWarning{
			when:    prep(fmt.Sprintf("warnings[%d].when", i), w.GetWhen(), manifest.VarItem, manifest.VarContext, manifest.VarOption),
			message: w.GetMessage(),
		})
	}
	if r := o.GetResolve(); r != nil {
		p.resolveLabel = prep("resolve.label", r.GetLabel(), manifest.VarItem)
		p.resolveDescription = prep("resolve.description", r.GetDescription(), manifest.VarItem)
	}
	if err != nil {
		return nil, err
	}
	if p.value == nil {
		return nil, fmt.Errorf("value is required")
	}
	return p, nil
}

// options projects every item of an action's output. An item whose value
// cannot be computed is left out and logged: one odd row must not empty the
// picker.
func (p *projector) options(data map[string]any, identity string, log *slog.Logger) ([]Option, error) {
	raw, err := p.items(map[string]any{manifest.VarOutput: data})
	if err != nil {
		return nil, &UpstreamError{Message: fmt.Sprintf("the integration's answer was not the list this type expects: %v", err)}
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, &UpstreamError{Message: "the integration's answer was not a list"}
	}
	out := make([]Option, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, item := range list {
		opt, ok := p.option(item, identity, log)
		if !ok || seen[opt.Value] {
			continue
		}
		seen[opt.Value] = true
		out = append(out, opt)
	}
	return out, nil
}

func (p *projector) option(item any, identity string, log *slog.Logger) (Option, bool) {
	vars := map[string]any{manifest.VarItem: item}
	raw, err := p.value(vars)
	value, ok := scalarString(raw)
	if err != nil || !ok || value == "" {
		log.Debug("input option skipped: no value", "error", err)
		return Option{}, false
	}
	opt := Option{Value: value, Label: value}
	if label := p.evalString(p.label, vars, log); label != "" {
		opt.Label = label
	}
	opt.Description = p.evalString(p.description, vars, log)
	opt.Group = p.evalString(p.group, vars, log)
	if len(p.annotations) > 0 {
		opt.Annotations = make(map[string]any, len(p.annotations))
		for name, e := range p.annotations {
			v, err := e(vars)
			if err != nil {
				log.Debug("input option annotation skipped", "annotation", name, "error", err)
				continue
			}
			opt.Annotations[name] = v
		}
	}
	if len(p.warnings) > 0 {
		wvars := map[string]any{
			manifest.VarItem:    item,
			manifest.VarContext: map[string]any{manifest.ContextActingIdentity: identity},
			manifest.VarOption: map[string]any{
				"value": opt.Value, "label": opt.Label, "description": opt.Description, "group": opt.Group,
				"annotations": annotationsOrEmpty(opt.Annotations),
			},
		}
		for _, w := range p.warnings {
			holds, err := w.when(wvars)
			if err != nil {
				log.Debug("input option warning skipped", "error", err)
				continue
			}
			if b, _ := holds.(bool); b {
				opt.Warnings = append(opt.Warnings, w.message)
			}
		}
	}
	return opt, true
}

// resolved is the option a lookup action's answer makes of value.
func (p *projector) resolved(value string, data map[string]any, log *slog.Logger) (Option, bool) {
	if p.resolveLabel == nil {
		return Option{}, false
	}
	vars := map[string]any{manifest.VarItem: data}
	label := p.evalString(p.resolveLabel, vars, log)
	if label == "" {
		return Option{}, false
	}
	return Option{Value: value, Label: label, Description: p.evalString(p.resolveDescription, vars, log)}, true
}

func (p *projector) evalString(e evaluator, vars map[string]any, log *slog.Logger) string {
	if e == nil {
		return ""
	}
	v, err := e(vars)
	if err != nil {
		log.Debug("input option projection skipped", "error", err)
		return ""
	}
	s, _ := scalarString(v)
	return s
}

func annotationsOrEmpty(a map[string]any) map[string]any {
	if a == nil {
		return map[string]any{}
	}
	return a
}

// bindingOf names the listing a page token belongs to.
func bindingOf(ref, connectionID string, scope map[string]any, query string) string {
	return hashParts("binding", ref, connectionID, canonical(scope), query)
}

// cacheKey names one action call by everything that decides its answer,
// including whose it is.
func cacheKey(userID, ref, connectionID string, scope map[string]any, query, upstream, kind string) string {
	return hashParts(userID, ref, connectionID, canonical(scope), query, upstream, kind)
}

func canonical(v map[string]any) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make([]any, 0, 2*len(keys))
	for _, k := range keys {
		ordered = append(ordered, k, v[k])
	}
	raw, _ := json.Marshal(ordered)
	return string(raw)
}

func hashParts(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s|", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
