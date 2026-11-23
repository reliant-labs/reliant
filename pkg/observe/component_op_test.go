package observe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// ticker is the tests' deterministic clock: a counter of nanoseconds since
// the Unix epoch that moves by step on every tick, so a call measured as two
// ticks lasts exactly step. It never consults the system clock.
type ticker struct {
	ns   int64
	step time.Duration
}

func (k *ticker) tick() time.Time {
	k.ns += int64(k.step)
	return time.Unix(0, k.ns)
}

// testArgs is what a generated capture looks like for
// Do(ctx, orderID string, note string, n int).
type testArgs struct {
	orderID string
	note    string
	n       int
}

func (a testArgs) RecordForensics(r *ForensicRecorder) {
	r.ID("orderID", a.orderID)
	r.Len("note", len(a.note))
	r.Int("n", int64(a.n))
	r.Type("cfg", "*checkout.Config")
}

func newRecordedChain(t *testing.T, opts ...ComponentSpanOption) (*ComponentChain, *tracetest.SpanRecorder) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	// A ticker unless the test brings its own (a later option wins): no
	// assertion depends on the system clock or where a call falls against a
	// time boundary.
	clock := &ticker{ns: 1_700_000_000 * int64(time.Second), step: time.Microsecond}
	opts = append([]ComponentSpanOption{withSpanClock(clock.tick)}, opts...)
	return NewComponentChain(TraceMiddlewareWith(tp.Tracer("test"), opts...)), rec
}

func attrsOf(kvs []attribute.KeyValue) map[string]attribute.Value {
	out := make(map[string]attribute.Value, len(kvs))
	for _, kv := range kvs {
		out[string(kv.Key)] = kv.Value
	}
	return out
}

func forensicsEvents(s sdktrace.ReadOnlySpan) []sdktrace.Event {
	var out []sdktrace.Event
	for _, e := range s.Events() {
		if e.Name == EventForensics {
			out = append(out, e)
		}
	}
	return out
}

func TestComponentOpSpanCarriesCodeLocation(t *testing.T) {
	chain, rec := newRecordedChain(t)
	op := NewOp("checkout.Do", "example.com/shop/internal/checkout.Service.Do", "internal/checkout/service.go")
	var seen *Op
	got, err := AroundOp(chain, context.Background(), op, NoArgs{}, func(ctx context.Context) (int, error) {
		seen = OpFromContext(ctx)
		return 7, nil
	})
	if err != nil || got != 7 {
		t.Fatalf("AroundOp = %d, %v", got, err)
	}
	if seen != op {
		t.Fatalf("OpFromContext inside the call = %v, want the call's op", seen)
	}
	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != "checkout.Do" {
		t.Fatalf("spans = %v", spans)
	}
	a := attrsOf(spans[0].Attributes())
	for k, want := range map[string]string{
		AttrForgeOp:      "checkout.Do",
		AttrCodeFunction: "example.com/shop/internal/checkout.Service.Do",
		AttrCodeFile:     "internal/checkout/service.go",
	} {
		if a[k].AsString() != want {
			t.Errorf("%s = %q, want %q", k, a[k].AsString(), want)
		}
	}
	if _, ok := a[AttrForgeSlow]; ok {
		t.Errorf("a fast call carries %s", AttrForgeSlow)
	}
	if len(forensicsEvents(spans[0])) != 0 {
		t.Errorf("a fast success recorded forensics")
	}
}

func TestComponentOpLegacyRunCarriesNoOp(t *testing.T) {
	chain, rec := newRecordedChain(t)
	op := NewOp("checkout.Do", "f", "x.go")
	_ = RunOp(chain, context.Background(), op, NoArgs{}, func(ctx context.Context) error {
		// A nested legacy call shadows the outer op.
		return chain.Run(ctx, "legacy.Inner", func(ctx context.Context) error {
			if OpFromContext(ctx) != nil {
				t.Errorf("legacy call inherited the outer op")
			}
			return errors.New("boom")
		})
	})
	for _, s := range rec.Ended() {
		if s.Name() != "legacy.Inner" {
			continue
		}
		if _, ok := attrsOf(s.Attributes())[AttrForgeOp]; ok {
			t.Errorf("legacy span carries %s", AttrForgeOp)
		}
		if len(forensicsEvents(s)) != 0 {
			t.Errorf("legacy span recorded forensics")
		}
	}
}

func TestComponentOpSlowMarkingAndForensics(t *testing.T) {
	clock := &ticker{step: 10 * time.Millisecond}
	chain, rec := newRecordedChain(t, withSpanClock(clock.tick), WithSlowWarmup(10))
	op := NewOp("checkout.Do", "", "")
	args := testArgs{orderID: "ord_123", note: "deliver to the back door", n: 3}
	call := func() {
		_ = RunOp(chain, context.Background(), op, args, func(context.Context) error { return nil })
	}
	for range 20 {
		call()
	}
	clock.step = 500 * time.Millisecond // 50x normal, above the floor
	call()
	spans := rec.Ended()
	for _, s := range spans[:20] {
		if _, ok := attrsOf(s.Attributes())[AttrForgeSlow]; ok {
			t.Fatalf("a call at its normal was marked slow")
		}
	}
	last := spans[len(spans)-1]
	if !attrsOf(last.Attributes())[AttrForgeSlow].AsBool() {
		t.Fatalf("a 50x call was not marked slow: %v", last.Attributes())
	}
	ev := forensicsEvents(last)
	if len(ev) != 1 {
		t.Fatalf("forensics events = %d, want 1", len(ev))
	}
	a := attrsOf(ev[0].Attributes)
	for k, want := range map[string]string{
		AttrForensicsReason:            "slow",
		ForensicsArgPrefix + "orderID": "ord_123",
		ForensicsArgPrefix + "note":    "[len=24]",
		ForensicsArgPrefix + "cfg":     "[type=*checkout.Config]",
	} {
		if a[k].AsString() != want {
			t.Errorf("%s = %q, want %q", k, a[k].AsString(), want)
		}
	}
	if a[ForensicsArgPrefix+"n"].AsInt64() != 3 {
		t.Errorf("n = %v", a[ForensicsArgPrefix+"n"])
	}
	for _, kv := range ev[0].Attributes {
		if strings.Contains(kv.Value.Emit(), "back door") {
			t.Fatalf("a non-ID string's value reached forensics: %s", kv.Key)
		}
	}
}

func TestComponentOpSlowNeedsTheFloor(t *testing.T) {
	clock := &ticker{step: 10 * time.Microsecond}
	chain, rec := newRecordedChain(t, withSpanClock(clock.tick), WithSlowWarmup(5))
	op := NewOp("checkout.Do", "", "")
	for range 10 {
		_ = RunOp(chain, context.Background(), op, NoArgs{}, func(context.Context) error { return nil })
	}
	clock.step = 5 * time.Millisecond // 500x normal, under the 100ms floor
	_ = RunOp(chain, context.Background(), op, NoArgs{}, func(context.Context) error { return nil })
	spans := rec.Ended()
	if _, ok := attrsOf(spans[len(spans)-1].Attributes())[AttrForgeSlow]; ok {
		t.Fatalf("a call under the floor was marked slow")
	}
}

func TestComponentOpForensicsOnServerFaultsOnlyAndRateLimited(t *testing.T) {
	clock := &ticker{step: time.Millisecond}
	chain, rec := newRecordedChain(t, withSpanClock(clock.tick), WithForensicsRate(2, time.Hour))
	op := NewOp("checkout.Do", "", "")
	args := testArgs{orderID: "ord_1"}
	run := func(err error) {
		_ = RunOp(chain, context.Background(), op, args, func(context.Context) error { return err })
	}
	run(svcerr.InvalidArgument("bad sku")) // user-class: no forensics
	run(context.Canceled)                  // canceled: no forensics
	for range 4 {
		run(errors.New("db down")) // server faults: 2 of 4 pass the bucket
	}
	var got []string
	for _, s := range rec.Ended() {
		for _, e := range forensicsEvents(s) {
			got = append(got, attrsOf(e.Attributes)[AttrForensicsReason].AsString())
		}
	}
	if strings.Join(got, ",") != "error,error" {
		t.Fatalf("forensics reasons = %v, want two errors (user/canceled skipped, rate limit 2)", got)
	}
}

func TestComponentOpForensicsOff(t *testing.T) {
	chain, rec := newRecordedChain(t, WithForensics(false))
	op := NewOp("checkout.Do", "", "")
	_ = RunOp(chain, context.Background(), op, testArgs{orderID: "x"}, func(context.Context) error { return errors.New("boom") })
	if n := len(forensicsEvents(rec.Ended()[0])); n != 0 {
		t.Fatalf("WithForensics(false) recorded %d events", n)
	}
}

type panickyArgs struct{}

func (panickyArgs) RecordForensics(*ForensicRecorder) { panic("capture bug") }

func TestComponentOpCapturePanicNeverChangesTheOutcome(t *testing.T) {
	chain, rec := newRecordedChain(t)
	want := errors.New("db down")
	err := RunOp(chain, context.Background(), NewOp("checkout.Do", "", ""), panickyArgs{}, func(context.Context) error { return want })
	if err != want {
		t.Fatalf("err = %v, want the call's own error", err)
	}
	if n := len(forensicsEvents(rec.Ended()[0])); n != 0 {
		t.Fatalf("a panicking capture recorded %d events", n)
	}
}

func TestForensicIDName(t *testing.T) {
	for name, want := range map[string]bool{
		"id": true, "ID": true, "orderID": true, "orderId": true, "orderIDs": true,
		"order_id": true, "order_ids": true, "UUID": true,
		"sessionID": false, "session_id": false, "tokenID": false, "apiKeyID": false, "otp_counter_id": false,
		"paid": false, "valid": false, "name": false, "": false, "Ident": false,
	} {
		if got := ForensicIDName(name); got != want {
			t.Errorf("ForensicIDName(%q) = %v, want %v", name, got, want)
		}
	}
}

// orderType builds, at runtime, the descriptor of
//
//	message Order {
//	  string order_id = 1;
//	  string email    = 2 [debug_redact = true];
//	  string note     = 3;
//	  int32  qty      = 4;
//	  Card   card     = 5;
//	  repeated string tags = 6;
//	}
//	message Card { string card_id = 1; string number = 2 [debug_redact = true]; Order parent = 3; }
func orderType(t *testing.T) (order, card protoreflect.MessageDescriptor) {
	t.Helper()
	redact := &descriptorpb.FieldOptions{DebugRedact: proto.Bool(true)}
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("forensics_test.proto"),
		Package: proto.String("forensicstest"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Order"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("order_id"), Number: proto.Int32(1), Type: str, Label: opt, JsonName: proto.String("orderId")},
					{Name: proto.String("email"), Number: proto.Int32(2), Type: str, Label: opt, JsonName: proto.String("email"), Options: redact},
					{Name: proto.String("note"), Number: proto.Int32(3), Type: str, Label: opt, JsonName: proto.String("note")},
					{Name: proto.String("qty"), Number: proto.Int32(4), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), Label: opt, JsonName: proto.String("qty")},
					{Name: proto.String("card"), Number: proto.Int32(5), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".forensicstest.Card"), Label: opt, JsonName: proto.String("card")},
					{Name: proto.String("tags"), Number: proto.Int32(6), Type: str, Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), JsonName: proto.String("tags")},
				},
			},
			{
				Name: proto.String("Card"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("card_id"), Number: proto.Int32(1), Type: str, Label: opt, JsonName: proto.String("cardId")},
					{Name: proto.String("number"), Number: proto.Int32(2), Type: str, Label: opt, JsonName: proto.String("number"), Options: redact},
					{Name: proto.String("parent"), Number: proto.Int32(3), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".forensicstest.Order"), Label: opt, JsonName: proto.String("parent")},
				},
			},
		},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatal(err)
	}
	return fd.Messages().ByName("Order"), fd.Messages().ByName("Card")
}

type protoArgs struct{ in proto.Message }

func (a protoArgs) RecordForensics(r *ForensicRecorder) { r.Proto("in", a.in) }

func TestComponentOpForensicsNeverRecordsARedactedProtoField(t *testing.T) {
	orderDesc, cardDesc := orderType(t)
	card := dynamicpb.NewMessage(cardDesc)
	card.Set(cardDesc.Fields().ByName("card_id"), protoreflect.ValueOfString("card_9"))
	card.Set(cardDesc.Fields().ByName("number"), protoreflect.ValueOfString("4111111111111111"))
	order := dynamicpb.NewMessage(orderDesc)
	f := orderDesc.Fields()
	order.Set(f.ByName("order_id"), protoreflect.ValueOfString("ord_42"))
	order.Set(f.ByName("email"), protoreflect.ValueOfString("ada@example.com"))
	order.Set(f.ByName("note"), protoreflect.ValueOfString("leave it with the neighbour"))
	order.Set(f.ByName("qty"), protoreflect.ValueOfInt32(2))
	order.Set(f.ByName("card"), protoreflect.ValueOfMessage(card))
	tags := order.Mutable(f.ByName("tags")).List()
	tags.Append(protoreflect.ValueOfString("gift"))
	// order.card.parent.card is the fourth level: past ForensicMaxDepth.
	deep := dynamicpb.NewMessage(orderDesc)
	deep.Set(f.ByName("card"), protoreflect.ValueOfMessage(dynamicpb.NewMessage(cardDesc)))
	card.Set(cardDesc.Fields().ByName("parent"), protoreflect.ValueOfMessage(deep))

	chain, rec := newRecordedChain(t)
	_ = RunOp(chain, context.Background(), NewOp("checkout.Place", "", ""), protoArgs{in: order}, func(context.Context) error {
		return errors.New("db down")
	})
	ev := forensicsEvents(rec.Ended()[0])
	if len(ev) != 1 {
		t.Fatalf("forensics events = %d", len(ev))
	}
	a := attrsOf(ev[0].Attributes)
	for k, want := range map[string]string{
		"forensics.arg.in.order_id":         "ord_42",
		"forensics.arg.in.note":             "[len=27]",
		"forensics.arg.in.card.card_id":     "card_9",
		"forensics.arg.in.tags":             "[count=1]",
		"forensics.arg.in.card.parent.card": "[message]",
	} {
		if a[k].AsString() != want {
			t.Errorf("%s = %q, want %q", k, a[k].AsString(), want)
		}
	}
	if a["forensics.arg.in.qty"].AsInt64() != 2 {
		t.Errorf("qty = %v", a["forensics.arg.in.qty"])
	}
	if _, ok := a["forensics.arg.in"]; ok {
		t.Errorf("a proto message records dotted field paths only, not a value of its own")
	}
	for _, kv := range ev[0].Attributes {
		v := kv.Value.Emit()
		if strings.Contains(v, "ada@") || strings.Contains(v, "4111") || strings.Contains(v, "neighbour") {
			t.Fatalf("redacted or non-ID value reached forensics: %s=%s", kv.Key, v)
		}
		if strings.HasSuffix(string(kv.Key), ".email") || strings.HasSuffix(string(kv.Key), ".number") {
			t.Fatalf("a debug_redact field was recorded at all: %s", kv.Key)
		}
	}
}

func TestForensicRecorderBounds(t *testing.T) {
	r := &ForensicRecorder{}
	r.ID("id", strings.Repeat("é", MaxForensicValue))
	if v := r.attrs[0].Value.AsString(); len(v) > MaxForensicValue+len("…") || !strings.HasSuffix(v, "…") {
		t.Fatalf("ID not bounded: %d bytes", len(v))
	}
	for i := range MaxForensicAttrs + 3 {
		r.Int("n"+string(rune('a'+i%26)), int64(i))
	}
	if r.args != MaxForensicAttrs || !r.truncated {
		t.Fatalf("args=%d truncated=%v", r.args, r.truncated)
	}
}

// BenchmarkComponentChainSuccess compares a fast successful call the way a
// generated decorator made it before (Around on a chain carrying method IDs)
// with the way it makes it now (AroundOp carrying an Op and arguments): the
// capture never runs, and allocs/op must match (go test -bench
// ComponentChainSuccess -benchmem ./pkg/observe/).
func BenchmarkComponentChainSuccess(b *testing.B) {
	ctx := context.Background()
	orderID, note := "ord_123", "deliver to the back door"
	op := NewOp("checkout.Do", "example.com/shop/internal/checkout.Service.Do", "internal/checkout/service.go")
	for _, sampled := range []bool{false, true} {
		sampler := sdktrace.NeverSample()
		if sampled {
			sampler = sdktrace.AlwaysSample()
		}
		tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler))
		chain := NewComponentChain(TraceMiddleware(tp.Tracer("bench"))).
			WithMethodIDs(map[string]string{"checkout.Do": "m_0123456789abcdef"})
		suffix := "/unsampled"
		if sampled {
			suffix = "/sampled"
		}
		b.Run("Around"+suffix, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = chain.Around(ctx, "checkout.Do", func(context.Context) (int, error) { return len(orderID) + len(note), nil })
			}
		})
		b.Run("AroundOp"+suffix, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = AroundOp(chain, ctx, op, testArgs{orderID: orderID, note: note, n: 1}, func(context.Context) (int, error) {
					return len(orderID) + len(note), nil
				})
			}
		})
	}
}

// timeArgs is what the generator emits for Book(ctx, roomID string, at
// time.Time, stay time.Duration, when *timestamppb.Timestamp, req proto):
// times and durations by type only.
type timeArgs struct {
	roomID string
	when   *timestamppb.Timestamp
	req    proto.Message
}

func (a timeArgs) RecordForensics(r *ForensicRecorder) {
	r.ID("roomID", a.roomID)
	r.Type("at", "Time")
	r.Type("stay", "Duration")
	r.Proto("when", a.when)
	r.Proto("req", a.req)
}

// TestComponentOpForensicsNeverRecordsATimeValue: an exact timestamp or
// duration in a request is user content (when someone booked, for how long),
// so neither a Go time nor a proto Timestamp/Duration field leaves its value.
func TestComponentOpForensicsNeverRecordsATimeValue(t *testing.T) {
	booked := time.Date(2031, 7, 14, 9, 30, 15, 0, time.UTC)
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("booking_test.proto"),
		Package:    proto.String("bookingtest"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/timestamp.proto", "google/protobuf/duration.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Booking"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: proto.String("start_at"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".google.protobuf.Timestamp"), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), JsonName: proto.String("startAt")},
				{Name: proto.String("stay"), Number: proto.Int32(2), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".google.protobuf.Duration"), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), JsonName: proto.String("stay")},
				{Name: proto.String("guests"), Number: proto.Int32(3), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), JsonName: proto.String("guests")},
			},
		}},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatal(err)
	}
	bookingDesc := fd.Messages().ByName("Booking")
	booking := dynamicpb.NewMessage(bookingDesc)
	booking.Set(bookingDesc.Fields().ByName("start_at"), protoreflect.ValueOfMessage(timestamppb.New(booked).ProtoReflect()))
	booking.Set(bookingDesc.Fields().ByName("stay"), protoreflect.ValueOfMessage(durationpb.New(73*time.Hour).ProtoReflect()))
	booking.Set(bookingDesc.Fields().ByName("guests"), protoreflect.ValueOfInt32(2))

	chain, rec := newRecordedChain(t)
	args := timeArgs{roomID: "room_7", when: timestamppb.New(booked), req: booking}
	_ = RunOp(chain, context.Background(), NewOp("booking.Book", "", ""), args, func(context.Context) error {
		return errors.New("db down")
	})
	ev := forensicsEvents(rec.Ended()[0])
	if len(ev) != 1 {
		t.Fatalf("forensics events = %d", len(ev))
	}
	a := attrsOf(ev[0].Attributes)
	for k, want := range map[string]string{
		"forensics.arg.roomID":       "room_7",
		"forensics.arg.at":           "[type=Time]",
		"forensics.arg.stay":         "[type=Duration]",
		"forensics.arg.when":         "[type=Timestamp]",
		"forensics.arg.req.start_at": "[type=Timestamp]",
		"forensics.arg.req.stay":     "[type=Duration]",
	} {
		if a[k].AsString() != want {
			t.Errorf("%s = %q, want %q", k, a[k].AsString(), want)
		}
	}
	if a["forensics.arg.req.guests"].AsInt64() != 2 {
		t.Errorf("guests = %v", a["forensics.arg.req.guests"])
	}
	// The booked moment and the stay, in every spelling a leak could take.
	secret := []string{"2031", fmt.Sprint(booked.Unix()), "73h", fmt.Sprint(int64((73 * time.Hour).Seconds())), "seconds", "nanos"}
	for _, kv := range ev[0].Attributes {
		for _, s := range secret {
			if strings.Contains(string(kv.Key), s) || strings.Contains(kv.Value.Emit(), s) {
				t.Fatalf("a time value reached forensics: %s=%s", kv.Key, kv.Value.Emit())
			}
		}
	}
}

func TestForensicSecretIDName(t *testing.T) {
	for name, want := range map[string]bool{
		"otp_counter_id": true, "sessionID": true, "apiTokenIds": true,
		"orderID": false, "id": false, "otp": false, "session": false, "tokenCount": false,
	} {
		if got := ForensicSecretIDName(name); got != want {
			t.Errorf("ForensicSecretIDName(%q) = %v, want %v", name, got, want)
		}
	}
}

// secretNumberArgs is what the generator emits for
// Verify(ctx, otpCounterID int64, sessionID uint32, attempt int, req proto).
type secretNumberArgs struct {
	otpCounterID int64
	sessionID    uint32
	attempt      int
	req          proto.Message
}

func (a secretNumberArgs) RecordForensics(r *ForensicRecorder) {
	r.IntDigits("otpCounterID", a.otpCounterID)
	r.UintDigits("sessionID", uint64(a.sessionID))
	r.Int("attempt", int64(a.attempt))
	r.Proto("req", a.req)
}

// TestComponentOpForensicsHidesSecretNamedNumbers: an ID-shaped name that
// mentions a credential hides a number's value as it hides a string's — a
// Go integer and a proto numeric field record "[len=N]", N the digit count,
// as the browser runtime does.
func TestComponentOpForensicsHidesSecretNamedNumbers(t *testing.T) {
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("otp_test.proto"),
		Package: proto.String("otptest"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Verify"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: proto.String("otp_counter_id"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_INT64.Enum(), Label: opt, JsonName: proto.String("otpCounterId")},
				{Name: proto.String("session_ids"), Number: proto.Int32(2), Type: descriptorpb.FieldDescriptorProto_TYPE_UINT32.Enum(), Label: opt, JsonName: proto.String("sessionIds")},
				{Name: proto.String("account_id"), Number: proto.Int32(3), Type: descriptorpb.FieldDescriptorProto_TYPE_INT64.Enum(), Label: opt, JsonName: proto.String("accountId")},
			},
		}},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatal(err)
	}
	md := fd.Messages().ByName("Verify")
	msg := dynamicpb.NewMessage(md)
	msg.Set(md.Fields().ByName("otp_counter_id"), protoreflect.ValueOfInt64(483920175))
	msg.Set(md.Fields().ByName("session_ids"), protoreflect.ValueOfUint32(7719))
	msg.Set(md.Fields().ByName("account_id"), protoreflect.ValueOfInt64(42))

	chain, rec := newRecordedChain(t)
	args := secretNumberArgs{otpCounterID: 381057264, sessionID: 90210, attempt: 3, req: msg}
	_ = RunOp(chain, context.Background(), NewOp("auth.Verify", "", ""), args, func(context.Context) error {
		return errors.New("db down")
	})
	ev := forensicsEvents(rec.Ended()[0])
	if len(ev) != 1 {
		t.Fatalf("forensics events = %d", len(ev))
	}
	a := attrsOf(ev[0].Attributes)
	for k, want := range map[string]string{
		"forensics.arg.otpCounterID":       "[len=9]",
		"forensics.arg.sessionID":          "[len=5]",
		"forensics.arg.req.otp_counter_id": "[len=9]",
		"forensics.arg.req.session_ids":    "[len=4]",
	} {
		if a[k].AsString() != want {
			t.Errorf("%s = %q, want %q", k, a[k].AsString(), want)
		}
	}
	if a["forensics.arg.attempt"].AsInt64() != 3 || a["forensics.arg.req.account_id"].AsInt64() != 42 {
		t.Errorf("a plain number lost its value: attempt=%v account_id=%v", a["forensics.arg.attempt"], a["forensics.arg.req.account_id"])
	}
	for _, kv := range ev[0].Attributes {
		for _, secret := range []string{"381057264", "90210", "483920175", "7719"} {
			if strings.Contains(kv.Value.Emit(), secret) {
				t.Fatalf("a secret-named number reached forensics: %s=%s", kv.Key, kv.Value.Emit())
			}
		}
	}
}
