// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

// The span attributes that name the run an RPC is about, so a trace can be
// found by the chat it served (`forge obs traces prod --attr chat.id=<id>`).
// chat.id is the key the streaming spans already use; logs carry the same id
// as telemetry.AttrChatID (reliant.chat.id), which the issue pipeline reads.
const (
	SpanAttrChatID     = "chat.id"
	SpanAttrWorkflowID = telemetry.AttrWorkflowID
	SpanAttrThreadID   = telemetry.AttrThreadID
)

// runFields maps the top-level fields every reliant proto names a run's ids
// by to the span attribute each becomes.
var runFields = []struct {
	field protoreflect.Name
	attr  string
}{
	{"chat_id", SpanAttrChatID},
	{"workflow_id", SpanAttrWorkflowID},
	{"thread_id", SpanAttrThreadID},
}

// runAttrs reads the run ids msg names, as span attributes: its top-level,
// set, non-empty string fields chat_id, workflow_id and thread_id, and, for
// a message that carries a chat rather than its id (StartChatResponse,
// GetChatResponse), the id of its top-level chat field. Anything else (no
// such field, not a proto message) names none.
func runAttrs(msg any) []attribute.KeyValue {
	m, ok := msg.(proto.Message)
	if !ok || m == nil {
		return nil
	}
	r := m.ProtoReflect()
	if !r.IsValid() {
		return nil
	}
	var out []attribute.KeyValue
	for _, f := range runFields {
		if v := stringField(r, f.field); v != "" {
			out = append(out, attribute.String(f.attr, v))
		}
	}
	if stringField(r, "chat_id") == "" {
		if fd := r.Descriptor().Fields().ByName("chat"); fd != nil && fd.Kind() == protoreflect.MessageKind &&
			fd.Cardinality() != protoreflect.Repeated && r.Has(fd) {
			if id := stringField(r.Get(fd).Message(), "id"); id != "" {
				out = append(out, attribute.String(SpanAttrChatID, id))
			}
		}
	}
	return out
}

// stringField is r's singular string field name, or "" when r has none set.
func stringField(r protoreflect.Message, name protoreflect.Name) string {
	fd := r.Descriptor().Fields().ByName(name)
	if fd == nil || fd.Kind() != protoreflect.StringKind || fd.Cardinality() == protoreflect.Repeated || !r.Has(fd) {
		return ""
	}
	return r.Get(fd).String()
}

// runScope is the telemetry.RunScope attrs name, for the logs written inside
// the RPC.
func runScope(attrs []attribute.KeyValue) telemetry.RunScope {
	var s telemetry.RunScope
	for _, kv := range attrs {
		switch string(kv.Key) {
		case SpanAttrChatID:
			s.ChatID = kv.Value.AsString()
		case SpanAttrWorkflowID:
			s.WorkflowID = kv.Value.AsString()
		case SpanAttrThreadID:
			s.ThreadID = kv.Value.AsString()
		}
	}
	return s
}

// runStamper stamps an RPC's server span with the run ids its messages name,
// each attribute once: the first message to name it (the request, before the
// response) wins.
type runStamper struct {
	span trace.Span
	mu   sync.Mutex
	seen map[attribute.Key]bool
}

func newRunStamper(span trace.Span) *runStamper {
	return &runStamper{span: span, seen: map[attribute.Key]bool{}}
}

// stamp sets the ids msg names that the span does not carry yet, and returns
// every id msg names.
func (s *runStamper) stamp(msg any) []attribute.KeyValue {
	attrs := runAttrs(msg)
	if len(attrs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fresh := make([]attribute.KeyValue, 0, len(attrs))
	for _, kv := range attrs {
		if !s.seen[kv.Key] {
			s.seen[kv.Key] = true
			fresh = append(fresh, kv)
		}
	}
	if len(fresh) > 0 {
		s.span.SetAttributes(fresh...)
	}
	return attrs
}
