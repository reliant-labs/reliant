package activity

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// WriterKind says who writes a declared event.
type WriterKind int

const (
	// WriterInterceptor: the interceptor writes the event after the handler
	// succeeds. The default.
	WriterInterceptor WriterKind = iota
	// WriterHandler: the handler calls [Record], normally inside its RunTx.
	WriterHandler
)

// String names the writer as the proto option spells it.
func (w WriterKind) String() string {
	if w == WriterHandler {
		return "HANDLER"
	}
	return "INTERCEPTOR"
}

// Declaration is an rpc's resolved activity declaration.
type Declaration struct {
	// None is the reason the rpc records no activity. When set, every other
	// field is zero.
	None string
	// Verb is the action token.
	Verb string
	// TargetKind is the target's type.
	TargetKind string
	// TargetID, TargetLabel, Env and Scope locate their values in the
	// request or response. A zero Path is absent (TargetID never is).
	TargetID, TargetLabel, Env, Scope Path
	// Detail lists the paths copied into Event.Detail.
	Detail []Path
	// Writer says who writes the event.
	Writer WriterKind
	// Implied reports a declaration derived from forge's CRUD shape rather
	// than written in an option.
	Implied bool
}

// Path is a resolved field path: the fields to walk, from the request or the
// response.
type Path struct {
	// Spec is the path as written ("response.release.version").
	Spec string
	// Response reports a path resolved against the response message.
	Response bool
	fields   []protoreflect.FieldDescriptor
}

// IsZero reports an absent path.
func (p Path) IsZero() bool { return p.Spec == "" }

// Key is the name a detail value is stored under: the path's last segment.
func (p Path) Key() string {
	if i := strings.LastIndexByte(p.Spec, '.'); i >= 0 {
		return p.Spec[i+1:]
	}
	return p.Spec
}

// responsePrefix marks a path resolved against the response.
const responsePrefix = "response."

// readPrefixes are the rpc-name verbs that read. A name starting with one,
// followed by an upper-case letter or nothing, is not a mutation.
var readPrefixes = []string{"BatchGet", "Get", "List", "Watch", "Search", "Query", "Count", "Describe"}

// IsMutating reports whether an rpc changes state and so must declare its
// activity (or `none`). It does unless its name starts with a read verb
// (Get, List, Watch, Search, Query, Count, Describe, BatchGet) or it declares
// protobuf's own read-only marker, `option idempotency_level = NO_SIDE_EFFECTS;`.
func IsMutating(md protoreflect.MethodDescriptor) bool {
	if opts, ok := md.Options().(*descriptorpb.MethodOptions); ok &&
		opts.GetIdempotencyLevel() == descriptorpb.MethodOptions_NO_SIDE_EFFECTS {
		return false
	}
	return !hasReadPrefix(string(md.Name()))
}

func hasReadPrefix(name string) bool {
	for _, p := range readPrefixes {
		if !strings.HasPrefix(name, p) {
			continue
		}
		rest := name[len(p):]
		if rest == "" || unicode.IsUpper(rune(rest[0])) {
			return true
		}
	}
	return false
}

// Resolve interprets md's activity declaration: the explicit
// `(forge.v1.method).activity` option, or, without one, the declaration
// forge's CRUD shape implies. It returns nil, nil when the rpc declares
// nothing, and an error naming every defect when the declaration is
// malformed. The interceptor and `forge lint` both call it, so they agree.
func Resolve(md protoreflect.MethodDescriptor) (*Declaration, error) {
	if opt := activityOption(md); opt != nil {
		return resolveExplicit(md, opt)
	}
	return impliedCRUD(md), nil
}

// activityOption returns md's activity option, or nil when it sets none.
func activityOption(md protoreflect.MethodDescriptor) *forgepb.ActivityOptions {
	opts, ok := md.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil || !proto.HasExtension(opts, forgepb.E_Method) {
		return nil
	}
	mo, ok := proto.GetExtension(opts, forgepb.E_Method).(*forgepb.MethodOptions)
	if !ok || mo == nil || mo.Activity == nil {
		return nil
	}
	return mo.Activity
}

func resolveExplicit(md protoreflect.MethodDescriptor, opt *forgepb.ActivityOptions) (*Declaration, error) {
	if opt.GetNone() != "" {
		if opt.GetVerb() != "" || opt.GetTarget() != nil || opt.GetEnv() != "" || opt.GetScope() != "" ||
			len(opt.GetDetail()) > 0 || opt.GetWriter() != forgepb.ActivityWriter_ACTIVITY_WRITER_UNSPECIFIED {
			return nil, fmt.Errorf("%s: activity `none` excludes every other field", md.FullName())
		}
		return &Declaration{None: strings.TrimSpace(opt.GetNone())}, nil
	}

	var errs []error
	d := &Declaration{Verb: opt.GetVerb(), TargetKind: opt.GetTarget().GetKind()}
	if opt.GetWriter() == forgepb.ActivityWriter_ACTIVITY_WRITER_HANDLER {
		d.Writer = WriterHandler
	}
	if !tokenRE.MatchString(d.Verb) {
		errs = append(errs, fmt.Errorf("verb %q must be a lower_snake token (\"promote\")", d.Verb))
	}
	if !tokenRE.MatchString(d.TargetKind) {
		errs = append(errs, fmt.Errorf("target.kind %q must be a lower_snake token (\"release\")", d.TargetKind))
	}
	resolve := func(field, spec string, required bool) Path {
		if spec == "" {
			if required {
				errs = append(errs, fmt.Errorf("%s is required", field))
			}
			return Path{}
		}
		p, err := resolvePath(md, spec)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", field, err))
		}
		return p
	}
	d.TargetID = resolve("target.id", opt.GetTarget().GetId(), true)
	d.TargetLabel = resolve("target.label", opt.GetTarget().GetLabel(), false)
	d.Env = resolve("env", opt.GetEnv(), false)
	d.Scope = resolve("scope", opt.GetScope(), false)
	for i, spec := range opt.GetDetail() {
		d.Detail = append(d.Detail, resolve(fmt.Sprintf("detail[%d]", i), spec, true))
	}
	if d.Writer == WriterInterceptor && (md.IsStreamingClient() || md.IsStreamingServer()) {
		errs = append(errs, errors.New("a streaming rpc's event cannot be written by the interceptor: declare `writer: ACTIVITY_WRITER_HANDLER` and call activity.Record, or `none`"))
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s: invalid activity declaration: %w", md.FullName(), errors.Join(errs...))
	}
	return d, nil
}

// resolvePath walks spec through md's request (or, with the response.
// prefix, its response) message.
func resolvePath(md protoreflect.MethodDescriptor, spec string) (Path, error) {
	p := Path{Spec: spec}
	msg := md.Input()
	rest := spec
	if strings.HasPrefix(spec, responsePrefix) {
		p.Response = true
		msg = md.Output()
		rest = strings.TrimPrefix(spec, responsePrefix)
	}
	segs := strings.Split(rest, ".")
	for i, seg := range segs {
		fd := msg.Fields().ByName(protoreflect.Name(seg))
		if fd == nil {
			return Path{}, fmt.Errorf("path %q: %s has no field %q", spec, msg.FullName(), seg)
		}
		if fd.IsList() || fd.IsMap() {
			return Path{}, fmt.Errorf("path %q: field %q is repeated; a path names one value", spec, seg)
		}
		last := i == len(segs)-1
		switch {
		case !last && fd.Kind() != protoreflect.MessageKind:
			return Path{}, fmt.Errorf("path %q: field %q is not a message, so the path cannot continue past it", spec, seg)
		case last && (fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind):
			return Path{}, fmt.Errorf("path %q: ends on message field %q; name a scalar inside it", spec, seg)
		case last && fd.Kind() == protoreflect.BytesKind:
			return Path{}, fmt.Errorf("path %q: ends on bytes field %q; name a string, number, bool or enum", spec, seg)
		}
		p.fields = append(p.fields, fd)
		if !last {
			msg = fd.Message()
		}
	}
	return p, nil
}

// crudNameRE splits a CRUD-named rpc into its operation and entity.
var crudNameRE = regexp.MustCompile(`^(Create|Update|Delete)([A-Z][A-Za-z0-9]*)$`)

// labelFields are the entity fields a CRUD target's label is taken from, in
// order of preference.
var labelFields = []protoreflect.Name{"name", "title", "display_name"}

// impliedCRUD derives the declaration forge's CRUD shape implies, or nil when
// md is not CRUD-named or its messages do not have the shape:
//
//	Create<E>  verb create  id response.<e>.id  (Create<E>Response carries an E)
//	Update<E>  verb update  id <e>.id           (Update<E>Request carries an E)
//	Delete<E>  verb delete  id id               (Delete<E>Request has a scalar id)
func impliedCRUD(md protoreflect.MethodDescriptor) *Declaration {
	if md.IsStreamingClient() || md.IsStreamingServer() {
		return nil
	}
	m := crudNameRE.FindStringSubmatch(string(md.Name()))
	if m == nil {
		return nil
	}
	op, entity := m[1], m[2]
	d := &Declaration{Verb: strings.ToLower(op), TargetKind: snake(entity), Implied: true}
	switch op {
	case "Create", "Update":
		carrier, prefix := md.Output(), responsePrefix
		if op == "Update" {
			carrier, prefix = md.Input(), ""
		}
		fd := entityField(carrier, entity)
		if fd == nil {
			return nil
		}
		id := fd.Message().Fields().ByName("id")
		if !scalarField(id) {
			return nil
		}
		d.TargetID = Path{Spec: prefix + string(fd.Name()) + ".id", Response: prefix != "", fields: []protoreflect.FieldDescriptor{fd, id}}
		for _, name := range labelFields {
			if lf := fd.Message().Fields().ByName(name); scalarField(lf) {
				d.TargetLabel = Path{Spec: prefix + string(fd.Name()) + "." + string(name), Response: prefix != "", fields: []protoreflect.FieldDescriptor{fd, lf}}
				break
			}
		}
	case "Delete":
		id := md.Input().Fields().ByName("id")
		if !scalarField(id) {
			return nil
		}
		d.TargetID = Path{Spec: "id", fields: []protoreflect.FieldDescriptor{id}}
	}
	return d
}

// entityField returns msg's single singular field typed by a message named
// entity, or nil when there is not exactly one.
func entityField(msg protoreflect.MessageDescriptor, entity string) protoreflect.FieldDescriptor {
	var found protoreflect.FieldDescriptor
	fields := msg.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.Kind() != protoreflect.MessageKind || fd.IsList() || fd.IsMap() || string(fd.Message().Name()) != entity {
			continue
		}
		if found != nil {
			return nil
		}
		found = fd
	}
	return found
}

// scalarField reports a singular field a path may end on.
func scalarField(fd protoreflect.FieldDescriptor) bool {
	if fd == nil || fd.IsList() || fd.IsMap() {
		return false
	}
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind, protoreflect.BytesKind:
		return false
	}
	return true
}

// snake converts a CamelCase entity name to its lower_snake target kind:
// DeployTree → deploy_tree, APIKey → api_key.
func snake(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			prevLower := i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]))
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if i > 0 && (prevLower || (nextLower && unicode.IsUpper(runes[i-1]))) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// value extracts p's value from msg as a string: "" when any message on the
// way is unset. Enums render as their value name.
func (p Path) value(msg protoreflect.Message) string {
	if p.IsZero() || msg == nil {
		return ""
	}
	for i, fd := range p.fields {
		if fd.ContainingMessage().FullName() != msg.Descriptor().FullName() {
			// The descriptor the path was resolved against is not this
			// message's: a mismatched call (never the interceptor's).
			return ""
		}
		if i < len(p.fields)-1 {
			if !msg.Has(fd) {
				return ""
			}
			msg = msg.Get(fd).Message()
			continue
		}
		return scalarString(fd, msg.Get(fd))
	}
	return ""
}

func scalarString(fd protoreflect.FieldDescriptor, v protoreflect.Value) string {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return v.String()
	case protoreflect.BoolKind:
		return strconv.FormatBool(v.Bool())
	case protoreflect.EnumKind:
		if ev := fd.Enum().Values().ByNumber(v.Enum()); ev != nil {
			return string(ev.Name())
		}
		return strconv.Itoa(int(v.Enum()))
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return strconv.FormatInt(v.Int(), 10)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return strconv.FormatUint(v.Uint(), 10)
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	}
	return ""
}
