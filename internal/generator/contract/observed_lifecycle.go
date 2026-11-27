// File: internal/generator/contract/observed_lifecycle.go
//
// Lifecycle methods are exempt from the observability decorator's chain.
//
// A component's lifecycle — Run, Drain, Close, Ready — is not a unit of work.
// Run returns when the process stops, so routing it through the chain opened
// one span and wrote one log line that lasted the whole process; Ready is a
// readiness probe called every few seconds; Drain and Close happen once, at
// shutdown, which the supervisor already logs phase by phase. So the
// decorator delegates them directly, as it does a `// forge:no-observe`
// method, and a long-running component needs no marker to be traced sanely.
//
// The set is exactly what the composition graph supervises
// (codegen.lifecycleOf, the lifecyclekit.Runner/Drainer/Closer/Readier
// shapes), plus io.Closer's Close: a method counts only when its NAME and
// SIGNATURE both match, so a business method that happens to be called Run —
// `Run(ctx context.Context, job Job) (Result, error)` — stays instrumented.
// `// forge:observe` on a lifecycle method routes it back through the chain.
package contract

// ObserveMarker opts one lifecycle method back into the decorator's chain:
// written above the interface method (or its implementation's doc, for a
// derived contract), it overrides the lifecycle exemption.
const ObserveMarker = "forge:observe"

// IsObserveComment reports whether one raw comment is the forge:observe
// marker: its whole text, spaced or not, as for forge:no-observe.
func IsObserveComment(raw string) bool {
	return trimContractCommentMarkers(raw) == ObserveMarker
}

// lifecycleExempt returns the names of the methods in methods (one
// interface's whole method set) that the decorator delegates directly
// because they are the component's lifecycle and carry no forge:observe.
func lifecycleExempt(methods []MethodDef) map[string]bool {
	byName := make(map[string]MethodDef, len(methods))
	for _, m := range methods {
		byName[m.Name] = m
	}
	ctxErr := func(name string) bool {
		m, ok := byName[name]
		return ok && isCtxErrMethod(m)
	}
	// A serverkit.Worker (Name() string, Start, Stop) lives in Start (or
	// RunContext) and shuts down in Stop. Its Run, when it has one, is ONE
	// tick that Start calls (a --kind cron worker) — real work, so it stays
	// observed, as codegen.lifecycleOf does not supervise it either.
	worker := isNameMethod(byName["Name"]) && ctxErr("Start") && ctxErr("Stop")

	out := map[string]bool{}
	mark := func(name string) {
		if m, ok := byName[name]; ok && !m.ForceObserve {
			out[name] = true
		}
	}
	if worker {
		for _, name := range []string{"Start", "Stop", "RunContext"} {
			if ctxErr(name) {
				mark(name)
			}
		}
	} else if ctxErr("Run") {
		mark("Run")
	}
	for _, name := range []string{"Drain", "Close", "Ready"} {
		if ctxErr(name) {
			mark(name)
		}
	}
	if m, ok := byName["Close"]; ok && isIOCloserMethod(m) {
		mark("Close")
	}
	return out
}

// isCtxErrMethod reports func(context.Context) error.
func isCtxErrMethod(m MethodDef) bool {
	return len(m.Params) == 1 && !m.Params[0].Variadic && m.Params[0].TypeExpr == "context.Context" &&
		len(m.Results) == 1 && m.Results[0].TypeExpr == "error"
}

// isIOCloserMethod reports Close() error, or a bare Close().
func isIOCloserMethod(m MethodDef) bool {
	return m.Name == "Close" && len(m.Params) == 0 &&
		(len(m.Results) == 0 || len(m.Results) == 1 && m.Results[0].TypeExpr == "error")
}

// isNameMethod reports Name() string.
func isNameMethod(m MethodDef) bool {
	return m.Name == "Name" && len(m.Params) == 0 && len(m.Results) == 1 && m.Results[0].TypeExpr == "string"
}

// lifecycleDoc is the comment above a lifecycle method's direct delegation in
// middleware_gen.go, so a reader of the decorator sees why it has no span.
func lifecycleDoc(name string) string {
	return "// " + name + " is part of the component's lifecycle, not a unit of work, so it\n" +
		"// delegates directly, around the chain. `// forge:observe` on the interface\n" +
		"// method routes it through the chain."
}
