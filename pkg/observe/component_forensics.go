// File: component_forensics.go — what a component span records about a call
// that failed or ran slow: forge.slow, and one redacted forge.forensics event.
//
// Redaction is decided at GENERATION time, deny by default. The generator
// reads each parameter's type and name and emits a capture method over only
// what it may record (internal/generator/contract/observed_forensics.go):
//
//	parameter                                     recorded as
//	ints, uints, floats, bools                    the value
//	secret-named number, not a quantity           "[len=N]": its digit count, ID-shaped or not
//	  (ForensicSecretNumberName; `max_tokens` is a quantity and keeps its value)
//	time.Time, time.Duration                      "[type=Time]", "[type=Duration]" — never the value
//	proto Timestamp, Duration                     "[type=Timestamp]", "[type=Duration]"
//	string named like an ID (ForensicIDValueName) the value, at most MaxForensicValue bytes
//	any other string, []byte                      "[len=42]" — the length only
//	slice, array, map, variadic                   "[count=3]" — the element count only
//	pointer to a proto message (a gen/ import)    walked here, under the same rules
//	a proto enum                                  the enum value's name, secret-named or not
//	anything else (structs, pointers, interfaces) "[type=*pkg.Type]" — the type only
//	`// forge:no-capture <param>` on the method    nothing
//
// A bool or an enum keeps its value under a secret name (`mfa_enabled`,
// `session_state`): either is one of a few values the schema itself lists,
// so it cannot carry anything a person typed. The name rules are in
// forensic_names.go.
//
// The encoding is the browser runtime's (@reliantlabs/forge-web-runtime,
// forge#2348), so client and server forensics read the same way.
//
// A proto message is walked with protoreflect into dotted field paths
// (forensics.arg.<param>.<field>…): a field marked [debug_redact = true] is
// omitted entirely, with no placeholder; other fields follow the parameter
// rules by field name, nested messages to ForensicMaxDepth levels (deeper:
// "[message]"). One event
// carries at most MaxForensicAttrs arguments (forensics.truncated=true says
// more were dropped). The event is rate-limited per op per process
// (WithForensicsRate), recorded only on a span that is being recorded, and a
// capture that panics is dropped rather than failing the call.
package observe

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// The forensics event and its attributes.
const (
	// EventForensics is the span event a failed or slow call records.
	EventForensics = "forge.forensics"
	// AttrForensicsReason is "error" or "slow".
	AttrForensicsReason = "forensics.reason"
	// ForensicsArgPrefix prefixes every recorded argument:
	// forensics.arg.<param>, forensics.arg.<param>.<field>… for a proto.
	ForensicsArgPrefix = "forensics.arg."
	// AttrForensicsTruncated is true when arguments past MaxForensicAttrs
	// were dropped.
	AttrForensicsTruncated = "forensics.truncated"
)

// Bounds on one forensics event.
const (
	// MaxForensicAttrs is the most arguments (and proto fields) one event
	// records.
	MaxForensicAttrs = 32
	// MaxForensicValue is the most bytes of one recorded ID string.
	MaxForensicValue = 128
	// ForensicMaxDepth is how many levels of nested proto message are
	// walked; a deeper message records "[message]".
	ForensicMaxDepth = 3
)

// ComponentSpanOption tunes TraceMiddlewareWith's slow marking and forensics.
type ComponentSpanOption func(*spanPolicy)

// WithSlowFactor sets how many times its op's normal a call must take to be
// slow (default DefaultSlowFactor). Zero or less turns slow marking off.
func WithSlowFactor(k float64) ComponentSpanOption {
	return func(p *spanPolicy) { p.factor = k }
}

// WithSlowFloor sets the absolute duration a call must also exceed to be slow
// (default DefaultSlowFloor).
func WithSlowFloor(d time.Duration) ComponentSpanOption {
	return func(p *spanPolicy) { p.floor = d }
}

// WithSlowWarmup sets how many calls an op must have seen before any is
// marked slow (default DefaultSlowWarmup).
func WithSlowWarmup(n int) ComponentSpanOption {
	return func(p *spanPolicy) {
		if n < 0 {
			n = 0
		}
		p.warmup = uint64(n)
	}
}

// WithForensics turns the forge.forensics event on or off (default on). The
// generator turns it off for a whole project with forge.yaml
// `observability.forensics: false`, by emitting no capture code; this is the
// runtime switch, for a seam that wants slow marking without forensics.
func WithForensics(enabled bool) ComponentSpanOption {
	return func(p *spanPolicy) { p.forensics = enabled }
}

// WithForensicsRate sets the per-op, per-process token bucket: at most burst
// events back to back, then one more per every (defaults
// DefaultForensicsBurst, DefaultForensicsEvery). burst <= 0 records none;
// every <= 0 never refills.
func WithForensicsRate(burst int, every time.Duration) ComponentSpanOption {
	return func(p *spanPolicy) { p.burst, p.every = burst, every }
}

// withSpanClock replaces the policy's clock (tests).
func withSpanClock(now func() time.Time) ComponentSpanOption {
	return func(p *spanPolicy) { p.now = now }
}

type spanPolicy struct {
	factor    float64
	logFactor float64
	floor     time.Duration
	warmup    uint64
	forensics bool
	burst     int
	every     time.Duration
	now       func() time.Time
}

func newSpanPolicy(opts []ComponentSpanOption) *spanPolicy {
	p := &spanPolicy{
		factor:    DefaultSlowFactor,
		floor:     DefaultSlowFloor,
		warmup:    DefaultSlowWarmup,
		forensics: true,
		burst:     DefaultForensicsBurst,
		every:     DefaultForensicsEvery,
		now:       time.Now,
	}
	for _, o := range opts {
		if o != nil {
			o(p)
		}
	}
	if p.factor > 0 {
		p.logFactor = math.Log(p.factor)
	} else {
		p.logFactor = math.Inf(1)
	}
	return p
}

// forensicsReason is why a call earns a forensics event, or "" when it does
// not. Only a server fault counts as an error: a cancellation, an expected
// close and a user-class refusal are answers, not failures to debug.
func forensicsReason(err error, slow bool) string {
	if err != nil {
		if _, closed := svcerr.CloseReason(err); !closed && svcerr.Classify(err) == svcerr.ClassServer {
			return "error"
		}
	}
	if slow {
		return "slow"
	}
	return ""
}

func (p *spanPolicy) recordForensics(span trace.Span, op *Op, f frameValue, reason string) {
	if !p.forensics || !span.IsRecording() || !op.limiter.allow(p.now(), p.burst, p.every) {
		return
	}
	r := &ForensicRecorder{attrs: make([]attribute.KeyValue, 0, 8)}
	r.attrs = append(r.attrs, attribute.String(AttrForensicsReason, reason))
	if !captureForensics(f, r) {
		return
	}
	if r.truncated {
		r.attrs = append(r.attrs, attribute.Bool(AttrForensicsTruncated, true))
	}
	span.AddEvent(EventForensics, trace.WithAttributes(r.attrs...))
}

// captureForensics runs the call's capture, reporting false when it panicked:
// forensics must never turn a call's outcome into a different one.
func captureForensics(f frameValue, r *ForensicRecorder) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	f.recordForensics(r)
	return true
}

// ForensicRecorder collects one call's classified arguments. The generated
// capture method calls one method per parameter, chosen at generation time;
// every value is bounded here.
type ForensicRecorder struct {
	attrs     []attribute.KeyValue
	args      int
	truncated bool
}

func (r *ForensicRecorder) add(kv attribute.KeyValue) {
	if r.args >= MaxForensicAttrs {
		r.truncated = true
		return
	}
	r.args++
	r.attrs = append(r.attrs, kv)
}

func forensicKey(name string) attribute.Key { return attribute.Key(ForensicsArgPrefix + name) }

// Int records a signed integer parameter.
func (r *ForensicRecorder) Int(name string, v int64) { r.add(forensicKey(name).Int64(v)) }

// Uint records an unsigned integer parameter (as a decimal string above
// math.MaxInt64).
func (r *ForensicRecorder) Uint(name string, v uint64) {
	if v > math.MaxInt64 {
		r.add(forensicKey(name).String(fmt.Sprint(v)))
		return
	}
	r.add(forensicKey(name).Int64(int64(v)))
}

// IntDigits records only the digit count of a secret-named integer
// (ForensicSecretNumberName): "[len=N]", N the length of its decimal form.
func (r *ForensicRecorder) IntDigits(name string, v int64) {
	r.Len(name, len(strconv.FormatInt(v, 10)))
}

// UintDigits is IntDigits for an unsigned integer.
func (r *ForensicRecorder) UintDigits(name string, v uint64) {
	r.Len(name, len(strconv.FormatUint(v, 10)))
}

// FloatDigits is IntDigits for a floating-point number: the length of the
// form JavaScript's String(v) prints, so the browser runtime records the
// same N for the same value.
func (r *ForensicRecorder) FloatDigits(name string, v float64) {
	r.Len(name, len(jsNumberString(v)))
}

// jsNumberString formats v as JavaScript's Number.prototype.toString does:
// the shortest round-trip digits, positional for 1e-6 <= |v| < 1e21 and
// exponential ("1e+21", "1.5e-7") outside it.
func jsNumberString(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "Infinity"
	case math.IsInf(v, -1):
		return "-Infinity"
	case v == 0:
		return "0" // -0 too
	}
	if a := math.Abs(v); a >= 1e-6 && a < 1e21 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	s := strconv.FormatFloat(v, 'e', -1, 64) // "1e+21", "1.5e-07"
	mant, exp, _ := strings.Cut(s, "e")
	sign, digits := exp[:1], strings.TrimLeft(exp[1:], "0")
	return mant + "e" + sign + digits
}

// Float records a floating-point parameter.
func (r *ForensicRecorder) Float(name string, v float64) { r.add(forensicKey(name).Float64(v)) }

// Bool records a boolean parameter.
func (r *ForensicRecorder) Bool(name string, v bool) { r.add(forensicKey(name).Bool(v)) }

// ID records an ID-shaped string parameter's value, bounded to
// MaxForensicValue bytes.
func (r *ForensicRecorder) ID(name, v string) { r.add(forensicKey(name).String(boundValue(v))) }

// Len records only the length of a string or []byte parameter: "[len=n]".
func (r *ForensicRecorder) Len(name string, n int) {
	r.add(forensicKey(name).String(fmt.Sprintf("[len=%d]", n)))
}

// Count records only the element count of a slice, array or map parameter:
// "[count=n]".
func (r *ForensicRecorder) Count(name string, n int) {
	r.add(forensicKey(name).String(fmt.Sprintf("[count=%d]", n)))
}

// Type records only a parameter's type: "[type=typ]".
func (r *ForensicRecorder) Type(name, typ string) {
	r.add(forensicKey(name).String("[type=" + typ + "]"))
}

// Named records a parameter of a named type the generator cannot see into:
// a proto enum's value name, otherwise its type only.
func (r *ForensicRecorder) Named(name, typ string, v any) {
	if e, ok := v.(protoreflect.Enum); ok {
		r.add(forensicKey(name).String(enumName(e.Descriptor(), e.Number())))
		return
	}
	r.Type(name, typ)
}

// Proto records a proto message parameter as dotted field paths: each
// populated field at forensics.arg.<name>.<field>, under the same rules as a
// parameter. A field marked debug_redact is never read and leaves no key. A
// nil message records nothing; m that is not a proto message records its
// type only.
func (r *ForensicRecorder) Proto(name string, m any) {
	pm, ok := m.(protoreflect.ProtoMessage)
	if !ok {
		r.Type(name, fmt.Sprintf("%T", m))
		return
	}
	msg := pm.ProtoReflect()
	if !msg.IsValid() {
		return
	}
	if t, ok := timeMessage(msg.Descriptor()); ok {
		r.Type(name, t)
		return
	}
	r.walk(name, msg, 1)
}

func (r *ForensicRecorder) walk(prefix string, msg protoreflect.Message, depth int) {
	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fieldRedacted(fd) {
			return true
		}
		r.field(prefix+"."+string(fd.Name()), fd, v, depth)
		return !r.truncated
	})
}

func (r *ForensicRecorder) field(name string, fd protoreflect.FieldDescriptor, v protoreflect.Value, depth int) {
	switch {
	case fd.IsList():
		r.Count(name, v.List().Len())
		return
	case fd.IsMap():
		r.Count(name, v.Map().Len())
		return
	}
	switch fd.Kind() {
	case protoreflect.BoolKind:
		r.Bool(name, v.Bool())
	case protoreflect.EnumKind:
		r.add(forensicKey(name).String(enumName(fd.Enum(), v.Enum())))
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		if ForensicSecretNumberName(string(fd.Name())) {
			r.IntDigits(name, v.Int())
		} else {
			r.Int(name, v.Int())
		}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		if ForensicSecretNumberName(string(fd.Name())) {
			r.UintDigits(name, v.Uint())
		} else {
			r.Uint(name, v.Uint())
		}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		if ForensicSecretNumberName(string(fd.Name())) {
			r.FloatDigits(name, v.Float())
		} else {
			r.Float(name, v.Float())
		}
	case protoreflect.StringKind:
		if ForensicIDValueName(string(fd.Name())) {
			r.ID(name, v.String())
		} else {
			r.Len(name, len(v.String()))
		}
	case protoreflect.BytesKind:
		r.Len(name, len(v.Bytes()))
	case protoreflect.MessageKind, protoreflect.GroupKind:
		if t, ok := timeMessage(fd.Message()); ok {
			r.Type(name, t)
			return
		}
		if depth >= ForensicMaxDepth {
			r.add(forensicKey(name).String("[message]"))
			return
		}
		r.walk(name, v.Message(), depth+1)
	default:
		r.Type(name, fd.Kind().String())
	}
}

// timeMessage reports a google.protobuf.Timestamp or Duration, recorded by
// type only: an exact time or duration in a request is user content.
func timeMessage(md protoreflect.MessageDescriptor) (string, bool) {
	switch md.FullName() {
	case "google.protobuf.Timestamp":
		return "Timestamp", true
	case "google.protobuf.Duration":
		return "Duration", true
	}
	return "", false
}

func fieldRedacted(fd protoreflect.FieldDescriptor) bool {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	return ok && opts.GetDebugRedact()
}

func enumName(ed protoreflect.EnumDescriptor, n protoreflect.EnumNumber) string {
	if ed != nil {
		if v := ed.Values().ByNumber(n); v != nil {
			return string(v.Name())
		}
	}
	return fmt.Sprint(int32(n))
}

func boundValue(v string) string {
	if len(v) <= MaxForensicValue {
		return v
	}
	cut := MaxForensicValue
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return v[:cut] + "…"
}
