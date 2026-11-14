package observe

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// The product-event contract. A product event is one INFO log record whose
// event.name attribute is "product.<name>"; the telemetry pipeline selects on
// that attribute, so the scope and logger name do not matter. The attribute
// keys below are the cross-repo seam: the web runtime, the Go emitter and the
// ClickHouse view must agree on them.
const (
	// EventNamePrefix starts every product event's event.name.
	EventNamePrefix = "product."
	// AttrEventName is the OTel event semconv attribute carrying the name.
	AttrEventName = "event.name"
	// AttrUserID is the authenticated user id; absent for anonymous callers.
	AttrUserID = "user.id"
	// AttrAppClient says which kind of emitter wrote the event.
	AttrAppClient = "app.client"
	// AttrAppVersion is the emitting app's version.
	AttrAppVersion = "app.version"
	// AttrPropPrefix prefixes every event property.
	AttrPropPrefix = "product."

	// AppClientBackend is the app.client value for events emitted by Track.
	AppClientBackend = "backend"
	// AppClientCLI is the app.client value for events a CLI emits
	// (pkg/observe/cliev).
	AppClientCLI = "cli"

	// MaxProductProps is the most properties one event keeps; the rest are dropped.
	MaxProductProps = 32
	// MaxProductPropValueLen is the longest property value in characters;
	// longer values are truncated.
	MaxProductPropValueLen = 256
)

// EnvProductEvents switches off the product events forge emits on the app's
// behalf (the generated CRUD handlers' <entity>_created|_updated|_deleted).
// "off" (or "false", "0") turns them off; anything else, or unset, leaves them
// on. KCL renders FORGE_PRODUCT_EVENTS=off when the env declares
// forge.ObservabilitySignals{product_events = False}. Track itself is never
// gated: an event the app's own code emits is a decision that code made.
const EnvProductEvents = "FORGE_PRODUCT_EVENTS"

var eventNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// autoProductEvents reads EnvProductEvents once: the process's environment
// does not change under it, and the check sits on every CRUD write. The read
// goes through otelenv.go's envValue, the one place this package reads the
// environment.
var autoProductEvents = sync.OnceValue(func() bool { return productEventsOn(envValue(EnvProductEvents)) })

// AutoProductEvents reports whether forge's automatic product events are on
// for this process (see EnvProductEvents). The value is read once.
func AutoProductEvents() bool { return autoProductEvents() }

func productEventsOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "false", "0":
		return false
	}
	return true
}

var appVersion atomic.Pointer[string]

// SetAppVersion sets the app.version stamped on product events. Setup calls it
// with Config.ServiceVersion; call it directly in a process that does not use
// Setup.
func SetAppVersion(v string) { appVersion.Store(&v) }

// ValidEventName reports whether name is a legal product event name: snake_case,
// starting with a letter, at most 64 characters.
func ValidEventName(name string) bool { return eventNameRE.MatchString(name) }

// Track records one product event: a named, countable thing a user did or that
// happened to them ("project_created", "subscription_started"). It writes an
// INFO record through the context's logger (FromContext) carrying
// event.name="product.<name>", app.client="backend", app.version, the actor's
// ids (ActorFrom: user.id and org.id from the claims forge's auth interceptor
// installs, or from WithActor; visitor.id from WithActor), and each attr as
// product.<key>.
//
// Properties are bounded the same way the telemetry pipeline bounds them: at
// most MaxProductProps are kept and each value is truncated to
// MaxProductPropValueLen characters. Group attrs are flattened to "group.key".
// Property values are stringified; keep them low-cardinality. A value that
// looks like personal data or a credential (LooksLikePII) is dropped, with one
// WARN per property key that names the key and never the value.
//
// Track never fails the caller. A name that is not snake_case is dropped with
// a warning, because the pipeline would drop it anyway. When the app
// registered a ConsentSource, an event it refuses (or cannot answer for) is
// dropped.
//
//	observe.Track(ctx, "project_created", slog.String("template", "go"), slog.Int("repos", 2))
func Track(ctx context.Context, name string, attrs ...slog.Attr) {
	if ctx == nil {
		ctx = context.Background()
	}
	trackAs(ctx, name, ActorFrom(ctx), AppClientBackend, attrs)
}

// trackAs is Track with the actor and client named by the caller rather than
// read from ctx: the one record shape every product event shares. TrackAuth
// uses it, because at sign-in the request is still anonymous; the CLI
// emitter (cliev) uses its record attributes through ProductEventAttrs.
func trackAs(ctx context.Context, name string, actor Actor, client string, attrs []slog.Attr) {
	logger := FromContext(ctx)
	if !ValidEventName(name) {
		logger.WarnContext(ctx, "product event dropped: name must be snake_case", slog.String("event", name))
		return
	}
	if !consentAllows(ctx, name, actor) {
		return
	}
	out := ProductEventAttrs(ctx, name, actor, client, attrs)
	logger.LogAttrs(ctx, slog.LevelInfo, EventNamePrefix+name, out...)
}

// ProductEventAttrs builds one product event's record attributes: the
// contract every emitter shares (event.name, the actor's ids, visitor.day
// from a trusted edge, app.client, app.version, and the bounded,
// PII-guarded product.<key> props). It does not validate name or check
// consent; Track does both before calling it. Exported for emitters that
// write the record themselves, such as pkg/observe/cliev.
func ProductEventAttrs(ctx context.Context, name string, actor Actor, client string, attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, 6+len(attrs))
	out = append(out, slog.String(AttrEventName, EventNamePrefix+name))
	out = actorAttrs(out, actor)
	// The visitor's daily hash, when the trusted edge stamped this request
	// (edge_context.go): it links a server-side event to the anonymous visit.
	if vd := EdgeContextFrom(ctx).VisitorDay; vd != "" {
		out = append(out, slog.String(AttrVisitorDay, vd))
	}
	out = append(out, slog.String(AttrAppClient, client))
	if v := appVersion.Load(); v != nil && *v != "" {
		out = append(out, slog.String(AttrAppVersion, *v))
	}
	props := 0
	guard := func(key, value string) bool {
		if looksLikePII(value) {
			warnPIIDropped(ctx, name, key)
			return false
		}
		return true
	}
	return appendProps(out, "", attrs, &props, guard)
}

func appendProps(out []slog.Attr, prefix string, attrs []slog.Attr, props *int, keep func(key, value string) bool) []slog.Attr {
	for _, a := range attrs {
		v := a.Value.Resolve()
		if v.Kind() == slog.KindGroup {
			p := prefix
			if a.Key != "" {
				p += a.Key + "."
			}
			out = appendProps(out, p, v.Group(), props, keep)
			continue
		}
		if a.Key == "" {
			continue
		}
		if *props >= MaxProductProps {
			return out
		}
		val := truncateRunes(v.String(), MaxProductPropValueLen)
		if !keep(prefix+a.Key, val) {
			continue
		}
		*props++
		out = append(out, slog.String(AttrPropPrefix+prefix+a.Key, val))
	}
	return out
}

func truncateRunes(s string, max int) string {
	if len(s) <= max || utf8.RuneCountInString(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}
