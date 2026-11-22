package release

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/telemetry"
)

// ShapeTelemetry is one declared telemetry target (forge.Telemetry) of a
// hosted env: a system on Reliant hosting whose metrics, logs and OTLP the
// PLATFORM collects, because forge runs no collector there. It is the
// normalized pkg/telemetry contract every other collector reads, so the
// control plane interprets a hosted target exactly as forge's own agent
// interprets a cluster one.
//
// Only targets whose Runtime is hosted are recorded ([ShapeTelemetryOf]):
// forge's own collectors reach every other runtime. An env that declares no
// hosted target has no entry, so every shape recorded before the field
// encodes and hashes exactly as it did.
//
// IT CARRIES NO VALUE. An OTLP env key is recorded by the fact it names
// (OTLPEnvValue.Ref), never by a value: the platform resolves the fact on its
// own runtime, and a literal value is already the workload's own env, which
// its hosted record delivers. Credentials are refused on a hosted env.
//
// THE JSON FIELD SET IS FROZEN under [BundleFeatureTelemetry]: a control
// plane that names the feature strict-decodes exactly these keys, so a field
// added to telemetry.Target (and so here) needs a NEW feature that withholds
// it from readers that predate it. TestShapeTelemetryKeysAreFrozen fails
// until it has one.
type ShapeTelemetry struct {
	telemetry.Target
}

// The caps on what a hosted env may ask the platform to pull. Owner-approved
// defaults (2026-11-20); per-plan caps, or overages, may replace them. A
// control plane that advertises its own ([BundleReader.TelemetryCaps]) is held
// to those instead when a bundle is recorded on it.
const (
	// DefaultHostedTelemetryMaxSampleLimit bounds MetricsScrape.SampleLimit:
	// the series one scrape may return before the platform drops the scrape.
	DefaultHostedTelemetryMaxSampleLimit = 5000
	// DefaultHostedTelemetryMaxPulledTargets bounds the targets per env the
	// platform must reach (telemetry.Target.Pulled: a scrape, a profile pull
	// or a check). A target that only pushes or parses logs costs no pull.
	DefaultHostedTelemetryMaxPulledTargets = 20
)

// TelemetryLimits are the caps a hosted env's declared telemetry is held to.
// A zero field is the default ([DefaultTelemetryLimits]).
type TelemetryLimits struct {
	MaxSampleLimit   int `json:"max_sample_limit,omitempty"`
	MaxPulledTargets int `json:"max_pulled_targets,omitempty"`
}

// DefaultTelemetryLimits is the caps a control plane that advertises none is
// held to, and the ones a shape is built under.
func DefaultTelemetryLimits() TelemetryLimits {
	return TelemetryLimits{
		MaxSampleLimit:   DefaultHostedTelemetryMaxSampleLimit,
		MaxPulledTargets: DefaultHostedTelemetryMaxPulledTargets,
	}
}

// Resolved is l with every unset cap defaulted. A nil l is the defaults.
func (l *TelemetryLimits) Resolved() TelemetryLimits {
	def := DefaultTelemetryLimits()
	if l == nil {
		return def
	}
	r := *l
	if r.MaxSampleLimit <= 0 {
		r.MaxSampleLimit = def.MaxSampleLimit
	}
	if r.MaxPulledTargets <= 0 {
		r.MaxPulledTargets = def.MaxPulledTargets
	}
	return r
}

// ShapeTelemetryOf is the shape's record of an env's normalized targets
// (KCLEntities.Telemetry): its hosted ones, each with its OTLP env reduced to
// the facts it names, sorted by service name. Nil when the env declares no
// hosted target. The input is not modified.
func ShapeTelemetryOf(targets []telemetry.Target) []ShapeTelemetry {
	var out []ShapeTelemetry
	for _, t := range targets {
		if t.Runtime == telemetry.RuntimeHosted {
			out = append(out, ShapeTelemetry{Target: t})
		}
	}
	return canonicalShapeTelemetry(out)
}

// canonicalShapeTelemetry normalizes each entry (so a producer that spells the
// defaults out hashes the same as one that leaves them off), keeps only the
// Ref of each OTLP env value, and sorts by service name. Nil when empty.
func canonicalShapeTelemetry(in []ShapeTelemetry) []ShapeTelemetry {
	if len(in) == 0 {
		return nil
	}
	targets := make([]telemetry.Target, len(in))
	for i, t := range in {
		targets[i] = t.Target
	}
	out := make([]ShapeTelemetry, 0, len(in))
	for _, t := range telemetry.Normalize(targets) {
		if t.OTLP != nil {
			env := make(map[string]telemetry.OTLPEnvValue, len(t.OTLP.Env))
			for k, v := range t.OTLP.Env {
				if v.Ref != "" {
					env[k] = telemetry.OTLPEnvValue{Ref: v.Ref}
				}
			}
			t.OTLP.Env = env
		}
		out = append(out, ShapeTelemetry{Target: t})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ServiceName < out[j].ServiceName })
	return slices.Clip(out)
}

// validateShapeTelemetry is the shape-side check: every entry is hosted, holds
// no OTLP env value, and passes telemetry.Validate, which carries the hosted
// rules (every scrape sets allow; no address, credential or profile pull; only
// declared ports). The caps are not checked here: they are the reader's
// policy, not the shape's ([HostedTelemetryProblems]).
func validateShapeTelemetry(ts []ShapeTelemetry) error {
	targets := make([]telemetry.Target, 0, len(ts))
	for _, t := range ts {
		if t.Runtime != telemetry.RuntimeHosted {
			return fmt.Errorf("%w: shape telemetry %s (service %s): runtime %q (the shape records only hosted targets)", ErrInvalid, t.Source, t.ServiceName, t.Runtime)
		}
		if t.OTLP != nil {
			for _, k := range sortedEnvKeys(t.OTLP.Env) {
				if t.OTLP.Env[k].Value != "" {
					return fmt.Errorf("%w: shape telemetry %s (service %s): otlp.env.%s carries a value; a shape records the fact a key names, never a value", ErrInvalid, t.Source, t.ServiceName, k)
				}
			}
		}
		targets = append(targets, t.Target)
	}
	if err := telemetry.Validate(targets, telemetry.Options{}); err != nil {
		return fmt.Errorf("%w: shape %v", ErrInvalid, err)
	}
	return nil
}

// HostedTelemetryProblems is what the hosted platform refuses of an env's
// declared targets beyond telemetry.Validate's rules, held to limits: a
// scrape whose sample_limit exceeds the cap, more pulled targets than the cap,
// and a platform-scoped target (the platform stamps every tenant signal with
// the env's identity, so a hosted target cannot be platform data). Only hosted
// targets are considered. Each problem names the target and field it is about;
// nil when there is none.
func HostedTelemetryProblems(targets []telemetry.Target, limits TelemetryLimits) []telemetry.Problem {
	limits = (&limits).Resolved()
	var problems []telemetry.Problem
	var pulled []telemetry.Target
	for _, t := range targets {
		if t.Runtime != telemetry.RuntimeHosted {
			continue
		}
		at := func(field, msg string) {
			problems = append(problems, telemetry.Problem{Source: t.Source, ServiceName: t.ServiceName, Field: field, Message: msg})
		}
		if t.Scope == telemetry.ScopePlatform {
			at("scope", "platform is not available on a hosted env: the platform stamps every signal of a hosted env with that env's identity, so it is project data")
		}
		for i, m := range t.Metrics {
			if m.SampleLimit > limits.MaxSampleLimit {
				at(fmt.Sprintf("metrics[%d].sample_limit", i), fmt.Sprintf(
					"%d exceeds the hosted cap of %d series per scrape: lower it, and narrow allow to the names you chart", m.SampleLimit, limits.MaxSampleLimit))
			}
		}
		if t.Pulled() {
			pulled = append(pulled, t)
		}
	}
	if len(pulled) > limits.MaxPulledTargets {
		names := make([]string, len(pulled))
		for i, t := range pulled {
			names[i] = t.ServiceName
		}
		over := pulled[limits.MaxPulledTargets]
		problems = append(problems, telemetry.Problem{Source: over.Source, ServiceName: over.ServiceName, Message: fmt.Sprintf(
			"is pulled target %d of %d: a hosted env may ask the platform to pull (scrape, profile or check) at most %d targets. "+
				"Pulled: %s. Drop the scrapes and checks you do not chart, or push OTLP instead",
			limits.MaxPulledTargets+1, len(pulled), limits.MaxPulledTargets, strings.Join(names, ", "))})
	}
	return problems
}

// TelemetryCaps is the caps this reader holds a hosted env's telemetry to: the
// ones its control plane advertised, each unset one defaulted.
func (r *BundleReader) TelemetryCaps() TelemetryLimits {
	if r == nil {
		return DefaultTelemetryLimits()
	}
	return r.Telemetry.Resolved()
}

// CollectsTelemetry reports whether a hosted env's declared telemetry is sent
// to this reader: it must decode the targets ([BundleFeatureTelemetry]) AND
// collect them ([CapabilityCollectsTelemetry]). Decoding alone is what every
// forge pin bump advertises, so it is not proof that anything collects. A nil
// reader, this forge's own ledger, does both.
func (r *BundleReader) CollectsTelemetry() bool {
	return len(r.missingTelemetryCapabilities()) == 0
}

// missingTelemetryCapabilities is what this reader does not advertise of the
// two that [BundleReader.CollectsTelemetry] needs, in that order.
func (r *BundleReader) missingTelemetryCapabilities() []string {
	var missing []string
	for _, c := range []string{BundleFeatureTelemetry, CapabilityCollectsTelemetry} {
		if !r.Supports(c) {
			missing = append(missing, strconv.Quote(c))
		}
	}
	return missing
}

// TelemetryWarning is what to tell the person deploying s when this reader
// does not collect its hosted telemetry ([BundleReader.CollectsTelemetry]):
// the declared hosted targets LimitShape leaves out, which the platform will
// therefore not collect, and the capability the control plane did not
// advertise. "" when there is nothing to say. A warning and not a refusal: the
// env still deploys and its workloads still run, only these signals go
// uncollected until the control plane is upgraded.
//
// Only targets the project declared are named. One forge rendered on its own
// (telemetry.OriginDefault: a ManagedDatabase's default) is left out just the
// same, silently: the person deploying asked for nothing, so every hosted
// deploy with a database would otherwise warn about a choice nobody made.
func (r *BundleReader) TelemetryWarning(env string, s Shape) string {
	missing := r.missingTelemetryCapabilities()
	if len(missing) == 0 {
		return ""
	}
	var declared []ShapeTelemetry
	for _, t := range s.Telemetry {
		if t.Origin != telemetry.OriginDefault {
			declared = append(declared, t)
		}
	}
	if len(declared) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "env %s declares telemetry this control plane does not collect yet (it does not advertise %s), "+
		"so it is left out of the release, and these targets will NOT be collected:", env, strings.Join(missing, " or "))
	for _, t := range declared {
		fmt.Fprintf(&b, "\n  - %s (%s): %s", t.ServiceName, t.Source, strings.Join(t.Signals(), ", "))
	}
	b.WriteString("\nThe deploy continues and the workloads run; their declared telemetry is collected once the control plane is upgraded.")
	return b.String()
}

func sortedEnvKeys(m map[string]telemetry.OTLPEnvValue) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
