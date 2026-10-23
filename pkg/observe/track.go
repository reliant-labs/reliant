package observe

import (
	"context"
	"log/slog"
	"regexp"
	"sync/atomic"
	"unicode/utf8"

	"github.com/reliant-labs/forge/pkg/authn"
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

	// MaxProductProps is the most properties one event keeps; the rest are dropped.
	MaxProductProps = 32
	// MaxProductPropValueLen is the longest property value in characters;
	// longer values are truncated.
	MaxProductPropValueLen = 256
)

var eventNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

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
// event.name="product.<name>", app.client="backend", app.version, user.id when
// ctx holds an authenticated user (the claims forge's auth interceptor
// installs), and each attr as product.<key>.
//
// Properties are bounded the same way the telemetry pipeline bounds them: at
// most MaxProductProps are kept and each value is truncated to
// MaxProductPropValueLen characters. Group attrs are flattened to "group.key".
// Property values are stringified; keep them low-cardinality and never put
// secrets or personal data in them.
//
// Track never fails the caller. A name that is not snake_case is dropped with
// a warning, because the pipeline would drop it anyway.
//
//	observe.Track(ctx, "project_created", slog.String("template", "go"), slog.Int("repos", 2))
func Track(ctx context.Context, name string, attrs ...slog.Attr) {
	if ctx == nil {
		ctx = context.Background()
	}
	logger := FromContext(ctx)
	if !ValidEventName(name) {
		logger.WarnContext(ctx, "product event dropped: name must be snake_case", slog.String("event", name))
		return
	}

	out := make([]slog.Attr, 0, 4+len(attrs))
	out = append(out, slog.String(AttrEventName, EventNamePrefix+name))
	if claims, ok := authn.ClaimsFromContext(ctx); ok && claims != nil && claims.UserID != "" {
		out = append(out, slog.String(AttrUserID, claims.UserID))
	}
	out = append(out, slog.String(AttrAppClient, AppClientBackend))
	if v := appVersion.Load(); v != nil && *v != "" {
		out = append(out, slog.String(AttrAppVersion, *v))
	}
	props := 0
	out = appendProps(out, "", attrs, &props)
	logger.LogAttrs(ctx, slog.LevelInfo, EventNamePrefix+name, out...)
}

func appendProps(out []slog.Attr, prefix string, attrs []slog.Attr, props *int) []slog.Attr {
	for _, a := range attrs {
		v := a.Value.Resolve()
		if v.Kind() == slog.KindGroup {
			p := prefix
			if a.Key != "" {
				p += a.Key + "."
			}
			out = appendProps(out, p, v.Group(), props)
			continue
		}
		if a.Key == "" {
			continue
		}
		if *props >= MaxProductProps {
			return out
		}
		*props++
		out = append(out, slog.String(AttrPropPrefix+prefix+a.Key, truncateRunes(v.String(), MaxProductPropValueLen)))
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
