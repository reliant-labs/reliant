package observe

import (
	"context"
	"log/slog"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
)

// LogKeyResource is the field every JSON log line carries the workload's
// OpenTelemetry resource identity under:
//
//	{"time":…,"level":"INFO","msg":"…","resource":{"service.name":"api","service.version":"v1.7.58",…},…}
//
// A log line read from stdout by a collector (filelog) has no SDK resource of
// its own: without this field its record would carry only what the collector
// derives from the pod, and none of what the traces and metrics of the same
// process carry. The node agents forge renders (and any collector following
// the same contract) lift the keys under this field into the record's
// RESOURCE, never its attributes, and leave a key a trusted source (the
// Kubernetes API, the log file's path) already set alone.
//
// Lines with no such field — a panic, a runtime fatal error, a library that
// writes to stdout directly, a non-forge image — carry no version: they pass
// through the collector unchanged.
const LogKeyResource = "resource"

// LogResourceKeys are the resource attributes stamped on every log line, in
// the order written. They are the workload's identity across signals: what
// the release, the environment and the replica a line came from are. Any
// other resource attribute stays on the SDK signals only.
var LogResourceKeys = []string{
	"service.name",
	"service.version",
	"service.namespace",
	"deployment.environment.name",
	"service.instance.id",
}

// processResource is the resource Setup built, as the attribute group a log
// line carries. Setup publishes it, so a logger built BEFORE Setup (the usual
// order: a process logs its own startup) switches to the very resource its
// traces and metrics use as soon as Setup runs.
var processResource atomic.Pointer[resourceStamp]

// resourceStamp is one resource, projected onto LogResourceKeys.
type resourceStamp struct{ attrs []slog.Attr }

func newResourceStamp(res *resource.Resource) *resourceStamp {
	var kv []any
	for _, k := range LogResourceKeys {
		if v, ok := res.Set().Value(attribute.Key(k)); ok && v.Emit() != "" {
			kv = append(kv, slog.String(k, v.Emit()))
		}
	}
	if len(kv) == 0 {
		return &resourceStamp{}
	}
	return &resourceStamp{attrs: []slog.Attr{slog.Group(LogKeyResource, kv...)}}
}

func publishResource(res *resource.Resource) { processResource.Store(newResourceStamp(res)) }

// newResourceHandler stamps every record next writes with the process
// resource (see LogKeyResource): Setup's once it has run, until then the one
// cfg and the OTEL_* environment describe, which is what Setup will build
// from the same inputs.
//
// The group is attached with next.WithAttrs, so a JSON handler encodes it
// once per resource rather than once per line, and it stays at the top level
// of the line whatever group a caller opens later. A text handler is left
// unstamped: text output is for a human at a terminal, not for a collector.
func newResourceHandler(next slog.Handler, cfg Config) slog.Handler {
	if _, ok := next.(*slog.TextHandler); ok {
		return next
	}
	fallback := &resourceStamp{}
	// The error is ignored on purpose: resource.New returns what it could
	// build alongside a partial-resource error (a malformed
	// OTEL_RESOURCE_ATTRIBUTES entry), and Setup reports that error itself.
	if res, _ := resourceFromConfig(context.Background(), cfg); res != nil {
		fallback = newResourceStamp(res)
	}
	return &resourceHandler{next: next, fallback: fallback, cache: new(atomic.Pointer[stamped])}
}

// resourceHandler defers next.WithAttrs/WithGroup until it knows which
// resource to stamp, then replays them onto next stamped with it, so the
// resource group precedes everything a caller added.
type resourceHandler struct {
	next     slog.Handler
	fallback *resourceStamp
	ops      []func(slog.Handler) slog.Handler
	cache    *atomic.Pointer[stamped]
}

// stamped is next, stamped with stamp and with the handler's ops replayed.
type stamped struct {
	stamp *resourceStamp
	h     slog.Handler
}

func (h *resourceHandler) current() slog.Handler {
	s := processResource.Load()
	if s == nil {
		s = h.fallback
	}
	if c := h.cache.Load(); c != nil && c.stamp == s {
		return c.h
	}
	out := h.next
	if len(s.attrs) > 0 {
		out = out.WithAttrs(s.attrs)
	}
	for _, op := range h.ops {
		out = op(out)
	}
	h.cache.Store(&stamped{stamp: s, h: out})
	return out
}

func (h *resourceHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *resourceHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.current().Handle(ctx, r)
}

func (h *resourceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.with(func(n slog.Handler) slog.Handler { return n.WithAttrs(attrs) })
}

func (h *resourceHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.with(func(n slog.Handler) slog.Handler { return n.WithGroup(name) })
}

func (h *resourceHandler) with(op func(slog.Handler) slog.Handler) slog.Handler {
	ops := make([]func(slog.Handler) slog.Handler, len(h.ops), len(h.ops)+1)
	copy(ops, h.ops)
	return &resourceHandler{next: h.next, fallback: h.fallback, ops: append(ops, op), cache: new(atomic.Pointer[stamped])}
}
