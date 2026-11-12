// Copyright (c) 2025 Reliant Labs

package viewertest

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Request builds an rpc's request for the fixture. RunService fills every
// id field it knows (Fill) without one.
type Request func(f *Fixture) proto.Message

// Fill sets the request fields that name the fixture's rows: chat_id (and
// session_id) the shared chat, project_id the shared project, plan_id its
// plan, message_id its assistant message, thread_id its thread, chat_ids and
// project_ids those one-element lists. Optional and plain string fields
// alike; nothing else is set.
func (f *Fixture) Fill(m proto.Message) {
	r := m.ProtoReflect()
	ids := map[protoreflect.Name]string{
		"chat_id":    f.ChatID,
		"session_id": f.ChatID,
		"project_id": f.ProjectID,
		"plan_id":    f.PlanID,
		"message_id": f.Transcript.AssistantMessageID,
		"thread_id":  f.Transcript.ThreadID,
	}
	fields := r.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.Kind() != protoreflect.StringKind {
			continue
		}
		name := fd.Name()
		if fd.IsList() {
			id, ok := ids[protoreflect.Name(strings.TrimSuffix(string(name), "s"))]
			if ok {
				r.Mutable(fd).List().Append(protoreflect.ValueOfString(id))
			}
			continue
		}
		if id, ok := ids[name]; ok {
			r.Set(fd, protoreflect.ValueOfString(id))
		}
	}
}

// answer is one caller's outcome: the error's code (0 for none) and the
// response.
type answer struct {
	code  connect.Code
	err   error
	resp  proto.Message
	panic any
}

func (a answer) String() string {
	switch {
	case a.panic != nil:
		return fmt.Sprintf("panic: %v", a.panic)
	case a.err != nil:
		return fmt.Sprintf("%v (code %v)", a.err, a.code)
	default:
		return "answered"
	}
}

func refused(c connect.Code) bool {
	return c == connect.CodeNotFound || c == connect.CodePermissionDenied
}

// RunService asks every rpc of service (a full name, reliant.v1.ChatService)
// that Matrix lists, on svc (the service's handler), as the owner, the viewer
// and the outsider, and holds each to its Matrix row:
//
//   - Answered: the owner and the viewer are answered and the outsider is
//     refused (NotFound or PermissionDenied).
//   - AsOutsider: the viewer gets exactly what the outsider gets, the same
//     code and an equal response. The owner is not asked: an owner's write
//     would change the fixture under the rows after it.
//   - Stream, Dedicated: not asked here.
//
// requests overrides the request of an rpc (by method name); every other is
// a new message with Fill applied. svc must have a method for every row: a
// service that gains an rpc without a row fails
// TestMatrixCoversEveryChatOrProjectRPC.
func RunService(t *testing.T, f *Fixture, svc any, service string, requests map[string]Request) {
	t.Helper()
	sd, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service))
	if err != nil {
		t.Fatalf("viewertest: no service %s: %v", service, err)
	}
	methods := sd.(protoreflect.ServiceDescriptor).Methods()
	var names []string
	for key := range Matrix {
		if svcName, method, _ := strings.Cut(key, "/"); svcName == service {
			names = append(names, method)
		}
	}
	if len(names) == 0 {
		t.Fatalf("viewertest: Matrix lists no rpc of %s", service)
	}
	// Answered rows first, so no row ever runs on a fixture another changed.
	sort.Slice(names, func(i, j int) bool {
		ri, rj := Matrix[service+"/"+names[i]], Matrix[service+"/"+names[j]]
		if (ri == Answered) != (rj == Answered) {
			return ri == Answered
		}
		return names[i] < names[j]
	})
	for name := range requests {
		if _, ok := Matrix[service+"/"+name]; !ok {
			t.Fatalf("viewertest: a request for %s/%s, which Matrix does not list", service, name)
		}
	}
	for _, name := range names {
		row := Matrix[service+"/"+name]
		if row == Stream || row == Dedicated {
			continue
		}
		md := methods.ByName(protoreflect.Name(name))
		if md == nil {
			t.Errorf("viewertest: Matrix lists %s/%s, which the service does not declare", service, name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			build := requests[name]
			if build == nil {
				build = func(f *Fixture) proto.Message {
					m, err := protoregistry.GlobalTypes.FindMessageByName(md.Input().FullName())
					if err != nil {
						t.Fatalf("no request type %s: %v", md.Input().FullName(), err)
					}
					msg := m.New().Interface()
					f.Fill(msg)
					return msg
				}
			}
			ask := func(ctx context.Context) answer { return invoke(t, svc, name, ctx, build(f)) }
			viewer, outsider := ask(f.Viewer), ask(f.Outsider)
			if outsider.panic != nil || viewer.panic != nil {
				t.Fatalf("viewer %v, outsider %v: a refusal must come before anything that panics", viewer, outsider)
			}
			switch row {
			case Answered:
				if owner := ask(f.Owner); owner.err != nil || owner.panic != nil {
					t.Fatalf("owner: %v, want answered", owner)
				}
				if viewer.err != nil {
					t.Fatalf("viewer: %v, want the read-only answer", viewer)
				}
				if !refused(outsider.code) {
					t.Fatalf("outsider: %v, want NotFound or PermissionDenied", outsider)
				}
			default:
				if viewer.code != outsider.code || !proto.Equal(viewer.resp, outsider.resp) {
					t.Fatalf("viewer %v (%v) differs from outsider %v (%v): a viewer of a shared row gains nothing here",
						viewer, viewer.resp, outsider, outsider.resp)
				}
			}
		})
	}
}

// invoke calls svc's method name with ctx and msg as a Connect unary handler
// (func(ctx, *connect.Request[T]) (*connect.Response[U], error)).
func invoke(t *testing.T, svc any, name string, ctx context.Context, msg proto.Message) (a answer) {
	t.Helper()
	method := reflect.ValueOf(svc).MethodByName(name)
	if !method.IsValid() {
		t.Fatalf("viewertest: %T has no method %s", svc, name)
	}
	mt := method.Type()
	if mt.NumIn() != 2 || mt.NumOut() != 2 {
		t.Fatalf("viewertest: %s is not a unary handler (%v)", name, mt)
	}
	req := reflect.New(mt.In(1).Elem())
	req.Elem().FieldByName("Msg").Set(reflect.ValueOf(msg))
	defer func() {
		if r := recover(); r != nil {
			a = answer{panic: r}
		}
	}()
	out := method.Call([]reflect.Value{reflect.ValueOf(ctx), req})
	if errV := out[1]; !errV.IsNil() {
		err := errV.Interface().(error)
		return answer{code: svcerr.Code(err), err: err}
	}
	if out[0].IsNil() {
		return answer{}
	}
	resp, _ := out[0].Elem().FieldByName("Msg").Interface().(proto.Message)
	return answer{resp: resp}
}
