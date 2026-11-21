// Package openfeature is the OpenFeature provider for Forge Changes: app code
// evaluates changes through the standard OpenFeature Go SDK
// (github.com/open-feature/go-sdk), with the default written at the call
// site, and a team can later swap providers without touching a call site.
//
//	store := changes.OpenFile(ctx, os.Getenv(changes.EnvFile), changes.Options{})
//	defer store.Close()
//	if err := of.SetProviderAndWait(forgeof.NewProvider(store)); err != nil { … }
//	client := of.NewDefaultClient()
//	evalCtx := of.NewEvaluationContext(userID, map[string]any{"plan": "paid"})
//	on, _ := client.BooleanValue(ctx, "new-checkout", false, evalCtx)
//
// The provider is a thin layer over a changes.Store: the Store decides and
// records the evaluation (the product event flag_evaluated, once per unit,
// change, run and variant per day), so do not also register a hook that
// records exposures for this provider.
//
// The evaluation context's targeting key is the user id; its string
// attributes carry the account id ("account"), the anonymous visitor id
// ("visitor") and the attribute a change's audience names. Attributes of any
// other type are dropped: a change's audience compares strings.
//
// How a forge reason reads in OpenFeature:
//
//	experiment, rollout                         SPLIT
//	preview                                     TARGETING_MATCH
//	everyone                                    STATIC
//	default, not_enrolled, ineligible, no_unit  DEFAULT
//	unknown                                     ERROR, FLAG_NOT_FOUND, the call site's default
//	invalid                                     ERROR, PARSE_ERROR, the call site's default
//
// A value of another type than the call asked for is ERROR, TYPE_MISMATCH,
// with the call site's default. Every resolution's flag metadata carries the
// forge reason ("reason"), the snapshot revision ("revision") and, for a
// rollout or experiment, the run ("run").
package openfeature

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	of "github.com/open-feature/go-sdk/openfeature"

	"github.com/reliant-labs/forge/pkg/changes"
)

// Provider is an OpenFeature FeatureProvider over a changes.Store.
type Provider struct {
	store *changes.Store
}

// NewProvider returns a provider that evaluates through store. The caller
// owns the store and closes it.
func NewProvider(store *changes.Store) *Provider {
	return &Provider{store: store}
}

// Metadata names the provider "forge".
func (p *Provider) Metadata() of.Metadata {
	return of.Metadata{Name: changes.ProviderName}
}

// Hooks returns none: the Store records evaluations itself.
func (p *Provider) Hooks() []of.Hook {
	return nil
}

// BooleanEvaluation resolves a change whose variants are JSON booleans.
func (p *Provider) BooleanEvaluation(ctx context.Context, flag string, defaultValue bool, flatCtx of.FlattenedContext) of.BoolResolutionDetail {
	v, d := resolve(ctx, p, flag, defaultValue, flatCtx, func(raw json.RawMessage) (bool, bool) {
		var b bool
		return b, json.Unmarshal(raw, &b) == nil
	})
	return of.BoolResolutionDetail{Value: v, ProviderResolutionDetail: d}
}

// StringEvaluation resolves a change whose variants are JSON strings.
func (p *Provider) StringEvaluation(ctx context.Context, flag string, defaultValue string, flatCtx of.FlattenedContext) of.StringResolutionDetail {
	v, d := resolve(ctx, p, flag, defaultValue, flatCtx, func(raw json.RawMessage) (string, bool) {
		var s string
		return s, json.Unmarshal(raw, &s) == nil
	})
	return of.StringResolutionDetail{Value: v, ProviderResolutionDetail: d}
}

// FloatEvaluation resolves a change whose variants are JSON numbers.
func (p *Provider) FloatEvaluation(ctx context.Context, flag string, defaultValue float64, flatCtx of.FlattenedContext) of.FloatResolutionDetail {
	v, d := resolve(ctx, p, flag, defaultValue, flatCtx, decodeFloat)
	return of.FloatResolutionDetail{Value: v, ProviderResolutionDetail: d}
}

// IntEvaluation resolves a change whose variants are JSON numbers with an
// integer value; any other number is a type mismatch.
func (p *Provider) IntEvaluation(ctx context.Context, flag string, defaultValue int64, flatCtx of.FlattenedContext) of.IntResolutionDetail {
	v, d := resolve(ctx, p, flag, defaultValue, flatCtx, func(raw json.RawMessage) (int64, bool) {
		f, ok := decodeFloat(raw)
		if !ok || f != math.Trunc(f) || f < math.MinInt64 || f >= math.MaxInt64 {
			return 0, false
		}
		return int64(f), true
	})
	return of.IntResolutionDetail{Value: v, ProviderResolutionDetail: d}
}

// ObjectEvaluation resolves a change of any variant type, decoded as
// encoding/json decodes into an any: objects are map[string]any, arrays
// []any, numbers float64.
func (p *Provider) ObjectEvaluation(ctx context.Context, flag string, defaultValue any, flatCtx of.FlattenedContext) of.InterfaceResolutionDetail {
	v, d := resolve(ctx, p, flag, defaultValue, flatCtx, func(raw json.RawMessage) (any, bool) {
		var v any
		return v, json.Unmarshal(raw, &v) == nil
	})
	return of.InterfaceResolutionDetail{Value: v, ProviderResolutionDetail: d}
}

// decodeFloat reads a JSON number; encoding/json never reads a JSON string
// such as "2" as one.
func decodeFloat(raw json.RawMessage) (float64, bool) {
	var f float64
	return f, json.Unmarshal(raw, &f) == nil
}

// resolve evaluates flag through the store and maps the result onto
// OpenFeature's resolution detail; decode reads a variant's value as T.
func resolve[T any](ctx context.Context, p *Provider, flag string, def T, flatCtx of.FlattenedContext, decode func(json.RawMessage) (T, bool)) (T, of.ProviderResolutionDetail) {
	r := p.store.Evaluate(ctx, flag, evalContext(flatCtx))
	meta := of.FlagMetadata{"reason": string(r.Reason), "revision": p.store.Revision()}
	if r.Run != "" {
		meta["run"] = r.Run
	}
	d := of.ProviderResolutionDetail{FlagMetadata: meta}

	switch r.Reason {
	case changes.ReasonUnknown:
		d.Reason, d.ResolutionError = of.ErrorReason, of.NewFlagNotFoundResolutionError(fmt.Sprintf("forge changes: no change %q in the snapshot", flag))
		return def, d
	case changes.ReasonInvalid:
		d.Reason, d.ResolutionError = of.ErrorReason, of.NewParseErrorResolutionError(fmt.Sprintf("forge changes: change %q failed validation", flag))
		return def, d
	}
	d.Reason = reason(r.Reason)
	if len(r.Value) == 0 {
		// The change declares no default variant: the call site's applies.
		return def, d
	}
	v, ok := decode(r.Value)
	if !ok {
		d.Reason, d.ResolutionError = of.ErrorReason, of.NewTypeMismatchResolutionError(fmt.Sprintf("forge changes: variant %q of %q is %s, not the type this call asked for", r.Variant, flag, r.Value))
		return def, d
	}
	d.Variant = r.Variant
	return v, d
}

// reason maps a forge reason that resolved a value onto OpenFeature's.
func reason(r changes.Reason) of.Reason {
	switch r {
	case changes.ReasonExperiment, changes.ReasonRollout:
		return of.SplitReason
	case changes.ReasonPreview:
		return of.TargetingMatchReason
	case changes.ReasonEveryone:
		return of.StaticReason
	}
	return of.DefaultReason
}

// evalContext reads the flattened OpenFeature context: the targeting key and
// every string attribute.
func evalContext(flat of.FlattenedContext) changes.EvalContext {
	var ec changes.EvalContext
	for k, v := range flat {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if k == of.TargetingKey {
			ec.TargetingKey = s
			continue
		}
		if ec.Attributes == nil {
			ec.Attributes = make(map[string]string, len(flat))
		}
		ec.Attributes[k] = s
	}
	return ec
}
