package changes

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// The product event an evaluation is recorded as, shared with the web
// runtime's exposure.ts and every other flag provider: a dashboard reads one
// event whatever evaluated the flag.
const (
	// FlagEvaluatedEvent is the product event name.
	FlagEvaluatedEvent = "flag_evaluated"
	// ProviderName is the provider property of the events this package
	// records, and the OpenFeature provider's name.
	ProviderName = "forge"
)

// maxRemembered bounds the evaluations a recorder remembers per UTC day.
// Past it the memory is cleared and starts again: an evaluation seen again
// may be recorded twice that day, which a readout's distinct-unit counts
// absorb, and memory stays bounded.
const maxRemembered = 10_000

// recorded reports whether a result of reason is recorded: enrolled results
// are a unit's assignment, and preview and everyone results let a dashboard
// show who saw what. A default, a unit outside the audience or the
// percentage, and a key or change that did not evaluate are not exposures.
func recorded(reason Reason) bool {
	switch reason {
	case ReasonRollout, ReasonExperiment, ReasonPreview, ReasonEveryone:
		return true
	}
	return false
}

// unitNames is units reversed: where the id was read, to the unit's name.
var unitNames = map[string]string{"": "user", "account": "account", "visitor": "visitor"}

// unit names the change's unit and reads its id from ec, or "" for both when
// the change has none.
func (s *Snapshot) unit(key string, ec EvalContext) (name, id string) {
	if s == nil {
		return "", ""
	}
	c, ok := s.changes[key]
	if !ok || c.invalid || !c.hasUnit {
		return "", ""
	}
	return unitNames[c.unitAttr], c.unitID(ec)
}

// recorder records Store evaluations as flag_evaluated, once per (unit,
// change, run, variant) per UTC day.
type recorder struct {
	mu   sync.Mutex
	day  string
	seen map[seenKey]struct{}
	now  func() time.Time
}

// seenKey is one remembered evaluation. Who it was about is kept as a
// SHA-256 digest (evalIdentity's), so the memory holds no raw ids.
type seenKey struct {
	who               [sha256.Size]byte
	key, run, variant string
}

// evalIdentity is who an evaluation is about, for the recorder's daily
// memory: the change's unit, namespaced by its name (a user id and an
// account id that happen to be equal are two units), else, for a change
// with no unit (everyone mode), the targeting key, else the actor ctx
// carries. "" when nothing names anyone: such evaluations are remembered
// as one.
func evalIdentity(ctx context.Context, unitName, unitID string, ec EvalContext) string {
	switch {
	case unitID != "":
		return "unit:" + unitName + "\x00" + unitID
	case ec.TargetingKey != "":
		return "targeting\x00" + ec.TargetingKey
	}
	a := observe.ActorFrom(ctx)
	switch {
	case a.UserID != "":
		return "user\x00" + a.UserID
	case a.AnonymousID != "":
		return "visitor\x00" + a.AnonymousID
	}
	return ""
}

func newRecorder() *recorder {
	return &recorder{now: time.Now}
}

// first reports whether k has not been recorded today, and remembers it.
func (rec *recorder) first(k seenKey) bool {
	day := rec.now().UTC().Format(time.DateOnly)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.day != day || rec.seen == nil || len(rec.seen) >= maxRemembered {
		rec.day = day
		rec.seen = make(map[seenKey]struct{})
	}
	if _, ok := rec.seen[k]; ok {
		return false
	}
	rec.seen[k] = struct{}{}
	return true
}

func (rec *recorder) record(ctx context.Context, snap *Snapshot, key string, ec EvalContext, r Result) {
	if !recorded(r.Reason) || r.Variant == "" {
		return
	}
	unitName, unitID := snap.unit(key, ec)
	who := sha256.Sum256([]byte(evalIdentity(ctx, unitName, unitID, ec)))
	if !rec.first(seenKey{who: who, key: key, run: r.Run, variant: r.Variant}) {
		return
	}
	attrs := []slog.Attr{
		slog.String("flag", key),
		slog.String("variant", r.Variant),
		slog.String("provider", ProviderName),
		slog.String("reason", string(r.Reason)),
		slog.String("revision", strconv.FormatInt(snap.Revision(), 10)),
	}
	if r.Run != "" {
		attrs = append(attrs, slog.String("run", r.Run))
	}
	if unitName != "" {
		attrs = append(attrs, slog.String("unit", unitName))
	}
	observe.Track(ctx, FlagEvaluatedEvent, attrs...)
}
