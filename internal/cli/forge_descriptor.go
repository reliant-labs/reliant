package cli

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	validatepb "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/activity"
	forgev1 "github.com/reliant-labs/forge/pkg/forgepb"
)

// descriptorStageDir is the staging directory under gen/ where each
// buf-plugin process drops one JSON fragment per proto file it processed. The
// parent process (runDescriptorGenerate) writes them out as the descriptor
// directory (codegen.WriteDescriptor) after buf finishes.
const descriptorStageDir = ".descriptor.d"

// ForgeDescriptor is the descriptor as codegen reads and writes it: one type,
// so the writer and the readers cannot disagree about a field.
type ForgeDescriptor = codegen.ForgeDescriptor

// descriptorFragment is one proto file's extraction, as a plugin process
// stages it for the parent.
type descriptorFragment struct {
	// File is the proto's path as the compiler names it (relative to the
	// buf module root, proto/ in a forge project).
	File     string                  `json:"file"`
	Services []codegen.ServiceDef    `json:"services"`
	Configs  []codegen.ConfigMessage `json:"configs"`
}

// generateDescriptor extracts services and configs from every proto file this
// plugin invocation was given, and stages one fragment per file under
// <descriptorOut>/<descriptorStageDir>/<sha1(path)>.json.
//
// The output directory is passed via the "descriptor_out" plugin option (set
// by runDescriptorGenerate in generate_orm.go). MergeDescriptorFragments
// writes the fragments out in the parent process after buf finishes.
//
// Why staged fragments instead of writing the descriptor here: buf may spawn
// several plugin processes at once, and the previous read-modify-write of a
// shared file (with an in-process syscall.Flock) silently raced, producing
// concatenated or truncated JSON. Each fragment is owned by one process, and
// the parent is the single writer of the descriptor directory.
func generateDescriptor(p *protogen.Plugin, descriptorOut string) error {
	if err := os.MkdirAll(descriptorOut, 0o755); err != nil {
		return fmt.Errorf("create descriptor output directory: %w", err)
	}
	stageDir := filepath.Join(descriptorOut, descriptorStageDir)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return fmt.Errorf("create descriptor stage directory: %w", err)
	}

	for _, f := range p.Files {
		if !f.Generate {
			continue
		}

		// `// forge:unmanaged` decides whether forge scaffolds a whole
		// service, so a marker that would silently do nothing — misspelled,
		// or written on an rpc or message — is refused here, before any
		// codegen reads the descriptor.
		if err := checkUnmanagedMarkers(f); err != nil {
			return err
		}
		// `// forge:custom` decides whether forge wires generic CRUD over an
		// rpc, so the same refusal covers it.
		if err := checkCustomMarkers(f); err != nil {
			return err
		}

		frag := descriptorFragment{File: f.Desc.Path()}
		for _, svc := range f.Services {
			frag.Services = append(frag.Services, extractService(f, svc))
		}

		// Entity protos are dead: SQL is the schema language and entities
		// are projected from the applied db/migrations schema. A message
		// still carrying the legacy (forge.v1.entity) annotation gets a
		// one-line migration notice — the annotation is otherwise IGNORED.
		for _, msg := range f.Messages {
			noticeLegacyEntityAnnotation(f, msg)
		}

		// Extract config messages. Walks nested message declarations too
		// so a component config block declared inside AppConfig (instead
		// of the conventional top-level declaration) still generates.
		for _, msg := range f.Messages {
			appendConfigMessages(&frag.Configs, msg)
		}

		// A file with nothing to say needs no fragment: the parent writes
		// its stamp-only descriptor file from the proto tree.
		if len(frag.Services) == 0 && len(frag.Configs) == 0 {
			continue
		}
		if err := stageDescriptorFragment(stageDir, frag); err != nil {
			return err
		}
	}
	return nil
}

// stageDescriptorFragment writes one fragment atomically, named by its proto
// path so a second invocation over the same file overwrites it.
func stageDescriptorFragment(stageDir string, frag descriptorFragment) error {
	sum := sha1.Sum([]byte(frag.File))
	fragmentName := hex.EncodeToString(sum[:]) + ".json"

	descBytes, err := json.MarshalIndent(frag, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write: write to a unique temp file in the same directory,
	// then rename. Rename is atomic on POSIX filesystems even across
	// concurrent processes.
	tmp, err := os.CreateTemp(stageDir, "frag-*.tmp")
	if err != nil {
		return fmt.Errorf("create descriptor fragment tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(descBytes); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write descriptor fragment: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close descriptor fragment: %w", err)
	}
	if err := os.Rename(tmpPath, filepath.Join(stageDir, fragmentName)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename descriptor fragment: %w", err)
	}
	return nil
}

// MergeDescriptorFragments writes the fragments staged under
// <descriptorOut>/<descriptorStageDir>/ out as projectDir's descriptor
// directory, gen/forge_descriptor/ (codegen.WriteDescriptor: one file per
// proto, each stamped with its own proto's fingerprint), then removes the
// staging dir. Called by runDescriptorGenerate in the parent process after
// buf returns.
//
// Idempotent. Returns nil without writing anything when no plugin process ran
// (no staging dir).
func MergeDescriptorFragments(descriptorOut, projectDir string) error {
	stageDir := filepath.Join(descriptorOut, descriptorStageDir)
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read descriptor stage dir: %w", err)
	}

	parts := map[string]codegen.DescriptorPart{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(stageDir, e.Name()))
		if err != nil {
			return fmt.Errorf("read descriptor fragment %s: %w", e.Name(), err)
		}
		var frag descriptorFragment
		if err := json.Unmarshal(data, &frag); err != nil {
			return fmt.Errorf("parse descriptor fragment %s: %w", e.Name(), err)
		}
		if frag.File == "" {
			return fmt.Errorf("descriptor fragment %s names no proto file", e.Name())
		}
		part := parts[frag.File]
		part.Services = append(part.Services, frag.Services...)
		part.Configs = append(part.Configs, frag.Configs...)
		parts[frag.File] = part
	}

	if err := codegen.WriteDescriptor(projectDir, parts); err != nil {
		return err
	}

	// Best-effort cleanup of the staging dir; failure here doesn't matter
	// for correctness because the next run starts with rm -rf.
	_ = os.RemoveAll(stageDir)
	return nil
}

// applyMethodOptions copies the proto-level (forge.v1.method) annotations
// onto a codegen.Method. Kept as a tiny helper so the descriptor's
// MethodOptions handling stays unit-testable without standing up a full
// protogen graph — see TestApplyMethodOptions_*.
func applyMethodOptions(method *codegen.Method, mo *forgev1.MethodOptions) {
	if mo == nil {
		return
	}
	if mo.AuthRequired != nil {
		method.AuthRequired = *mo.AuthRequired
	}
	if len(mo.Errors) > 0 {
		method.Errors = append([]string(nil), mo.Errors...)
	}
	if mo.Timeout != nil {
		method.Timeout = mo.Timeout.AsDuration()
	}
	// auth_required (above) is the one access annotation forge reads: it
	// feeds pkg/middleware/procedures_gen.go, which the auth interceptor
	// enforces fail-closed. Authorization stays handler logic. timeout feeds
	// the same file's ProcedureDeadlines, the chain's Deadline slot.
}

// methodActivity records md's activity declaration through
// forge/pkg/activity — the resolver the runtime interceptor uses — so the
// lint (forgeconv-activity-declared) reads the runtime's own verdict rather
// than re-deriving the rules.
func methodActivity(md protoreflect.MethodDescriptor) *codegen.MethodActivity {
	a := &codegen.MethodActivity{Mutating: activity.IsMutating(md)}
	decl, err := activity.Resolve(md)
	switch {
	case err != nil:
		a.Error = err.Error()
	case decl == nil:
	case decl.None != "":
		a.Declared, a.None = true, decl.None
	default:
		a.Declared, a.Verb, a.TargetKind, a.Implied, a.Writer = true, decl.Verb, decl.TargetKind, decl.Implied, decl.Writer.String()
	}
	return a
}

// methodHasNoSideEffects reports `option idempotency_level = NO_SIDE_EFFECTS;`
// on md — protobuf's standard declaration that an rpc only reads.
func methodHasNoSideEffects(md protoreflect.MethodDescriptor) bool {
	opts, ok := md.Options().(*descriptorpb.MethodOptions)
	return ok && opts.GetIdempotencyLevel() == descriptorpb.MethodOptions_NO_SIDE_EFFECTS
}

// serviceAudience reads `(forge.v1.service).audience`, defaulting to "user"
// — the same default serverkit.ServiceAudience applies at boot.
func serviceAudience(svc *protogen.Service) string {
	if opts := svc.Desc.Options(); opts != nil {
		if so, ok := proto.GetExtension(opts, forgev1.E_Service).(*forgev1.ServiceOptions); ok && so.GetAudience() != "" {
			return so.GetAudience()
		}
	}
	return "user"
}

// extractService builds a codegen.ServiceDef from a protogen.Service.
// goPkgName derives the short Go package name from a protogen.File.
// For a go_package like "github.com/example/gen/services/api/v1;apiv1" it
// returns "apiv1". When no alias is present it falls back to the last path
// segment of the import path.
func goPkgName(file *protogen.File) string {
	// protogen gives us the package name directly — it already resolves the
	// alias from the go_package option.
	if pn := string(file.GoPackageName); pn != "" {
		return pn
	}
	// Fallback: last path segment of the import path.
	p := string(file.GoImportPath)
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

func extractService(file *protogen.File, svc *protogen.Service) codegen.ServiceDef {
	sd := codegen.ServiceDef{
		Name:      string(svc.Desc.Name()),
		Package:   string(file.Desc.Package()),
		GoPackage: string(file.GoImportPath),
		PkgName:   goPkgName(file),
		ProtoFile: file.Desc.Path(),
		Messages:  make(map[string][]codegen.MessageFieldDef),
		Unmanaged: codegen.CommentMarksUnmanaged(serviceMarkerComments(svc)),
		Audience:  serviceAudience(svc),
	}

	for _, m := range svc.Methods {
		// Default to fail-closed: an unannotated method requires auth.
		// Methods that explicitly set `auth_required = false` in their
		// (forge.v1.method) annotation can opt out — the proto field is
		// `optional bool` so we distinguish unset from explicit false.
		// InputProtoFile / OutputProtoFile record the file that
		// physically declares the message. For same-file RPCs this
		// equals file.Desc.Path(); for cross-file refs (e.g. an RPC in
		// services/users/v1/users.proto returning a shared/v1/types.proto
		// Page) it differs, and the frontend hooks generator uses this
		// to import the symbol from the correct _pb.ts file rather than
		// the service's own _pb.ts.
		method := codegen.Method{
			Name:            string(m.Desc.Name()),
			InputType:       string(m.Input.Desc.Name()),
			OutputType:      string(m.Output.Desc.Name()),
			InputTypeFQ:     string(m.Input.Desc.FullName()),
			OutputTypeFQ:    string(m.Output.Desc.FullName()),
			ClientStreaming: m.Desc.IsStreamingClient(),
			ServerStreaming: m.Desc.IsStreamingServer(),
			AuthRequired:    true,
			InputProtoFile:  m.Input.Desc.ParentFile().Path(),
			OutputProtoFile: m.Output.Desc.ParentFile().Path(),
			Custom:          codegen.CommentMarksCustom(rpcMarkerComments(m)),
			NoSideEffects:   methodHasNoSideEffects(m.Desc),
			Idempotency:     m.Desc.Options().(*descriptorpb.MethodOptions).GetIdempotencyLevel().String(),
		}

		// Read method-level options
		if methodOpts := m.Desc.Options(); methodOpts != nil {
			ext := proto.GetExtension(methodOpts, forgev1.E_Method)
			if ext != nil {
				if mo, ok := ext.(*forgev1.MethodOptions); ok && mo != nil {
					applyMethodOptions(&method, mo)
				}
			}
		}

		method.Activity = methodActivity(m.Desc)

		sd.Methods = append(sd.Methods, method)

		// Extract message field definitions for input and output types
		extractMessageFields(sd.Messages, m.Input)
		extractMessageFields(sd.Messages, m.Output)

		// Extract the deep type graph (transitively reachable messages
		// + enums, keyed by fully-qualified name) for full JSON-Schema
		// emission.
		extractMessageSchema(&sd, m.Input)
		extractMessageSchema(&sd, m.Output)
	}

	return sd
}

// extractMessageSchema records msg's fields into sd.Schemas (keyed by
// fully-qualified name) and recurses into every message/enum the fields
// reference, so sd.Schemas ends up holding the complete type graph
// reachable from the service's RPC inputs/outputs.
//
// Recursion safety: the fully-qualified name is inserted into
// sd.Schemas BEFORE walking the fields, so self-referential messages
// (Tree → children []Tree) and mutually-recursive pairs terminate — the
// second visit hits the early-return.
//
// Well-known types (google.protobuf.*) are deliberately NOT recorded:
// their protojson encodings are fixed (Timestamp → RFC 3339 string,
// Struct → arbitrary object, ...) and schema emitters map them
// statically. Recording e.g. Struct's internal fields would describe
// the proto representation, not the JSON wire shape.
func extractMessageSchema(sd *codegen.ServiceDef, msg *protogen.Message) {
	fq := string(msg.Desc.FullName())
	if strings.HasPrefix(fq, "google.protobuf.") {
		return
	}
	if sd.Schemas == nil {
		sd.Schemas = make(map[string][]codegen.SchemaFieldDef)
	}
	if sd.SchemaFiles == nil {
		sd.SchemaFiles = make(map[string]string)
	}
	if _, done := sd.Schemas[fq]; done {
		return
	}
	// Record the file that physically declares this message so downstream
	// codegen can import its generated `*Schema` from the right _pb module
	// when the message lives in a different proto file than the service.
	sd.SchemaFiles[fq] = msg.Desc.ParentFile().Path()
	sd.Schemas[fq] = nil // recursion guard — overwritten with the real fields below

	fields := make([]codegen.SchemaFieldDef, 0, len(msg.Fields))
	for _, f := range msg.Fields {
		fd := codegen.SchemaFieldDef{
			Name:     string(f.Desc.Name()),
			Optional: f.Desc.HasOptionalKeyword(),
		}
		// Real oneof membership only — proto3 `optional` is implemented
		// as a synthetic single-member oneof and must not surface as an
		// exclusivity constraint.
		if f.Oneof != nil && !f.Oneof.Desc.IsSynthetic() {
			fd.Oneof = string(f.Oneof.Desc.Name())
		}
		switch {
		case f.Desc.IsMap():
			fd.Kind = "map"
			fd.MapKeyKind = protoKindToString(f.Desc.MapKey().Kind())
			val := f.Desc.MapValue()
			fd.MapValueKind = protoKindToString(val.Kind())
			// For message/enum values, f.Message is the synthetic map
			// entry whose Fields[1] is the value — recurse through it so
			// the value type lands in the graph too.
			switch val.Kind() {
			case protoreflect.MessageKind:
				fd.MapValueTypeName = string(val.Message().FullName())
				if f.Message != nil && len(f.Message.Fields) == 2 && f.Message.Fields[1].Message != nil {
					extractMessageSchema(sd, f.Message.Fields[1].Message)
				}
			case protoreflect.EnumKind:
				fd.MapValueTypeName = string(val.Enum().FullName())
				if f.Message != nil && len(f.Message.Fields) == 2 && f.Message.Fields[1].Enum != nil {
					recordEnum(sd, f.Message.Fields[1].Enum)
				}
			}
		case f.Desc.Kind() == protoreflect.MessageKind, f.Desc.Kind() == protoreflect.GroupKind:
			fd.Kind = "message"
			fd.Repeated = f.Desc.IsList()
			fd.TypeName = string(f.Desc.Message().FullName())
			if f.Message != nil {
				extractMessageSchema(sd, f.Message)
			}
		case f.Desc.Kind() == protoreflect.EnumKind:
			fd.Kind = "enum"
			fd.Repeated = f.Desc.IsList()
			fd.TypeName = string(f.Desc.Enum().FullName())
			if f.Enum != nil {
				recordEnum(sd, f.Enum)
			}
		default:
			fd.Kind = protoKindToString(f.Desc.Kind())
			fd.Repeated = f.Desc.IsList()
		}
		fd.Validate = fieldConstraintsFromDescriptor(f.Desc)
		fd.Secret = fieldHasSecretMarker(f)
		fd.ReadOnly = fieldHasReadOnlyMarker(f)
		fd.Generated = fieldGeneratedExpr(f)
		fd.Guards = fieldGuardTargets(f)
		fields = append(fields, fd)
	}
	sd.Schemas[fq] = fields
}

// secretFieldMarkerRE matches the `forge:secret` field marker inside a
// proto leading/trailing comment. buf strips the `//` before handing the
// text to protogen, so — unlike the raw scan's entityMarkerRE — the token
// is matched WITHOUT the leading slashes. Spacing/prose variants tolerated
// (`forge:secret`, `  forge:secret — db creds`), any line of a multi-line
// comment block.
//
// The marker NAME comes from codegen.KnownProtoMarkers (proto_markers.go) so
// this pass and `forge lint --proto-markers` share one vocabulary. forge:secret
// is read HERE and nowhere else — the raw scanner never looks for it — which is
// why the registry has to be the union across passes rather than any one
// scanner's list.
var secretFieldMarkerRE = codegen.ProtoMarkerCommentRE(codegen.ProtoMarkerSecret)

// fieldHasSecretMarker reports whether a field carries the `// forge:secret`
// marker as its LEADING comment (the documented site) or a TRAILING one
// (`string token = 2; // forge:secret` — the natural inline spelling). The
// marker keeps the column but strips the field from read responses; see
// SchemaFieldDef.Secret.
func fieldHasSecretMarker(f *protogen.Field) bool {
	return secretFieldMarkerRE.MatchString(string(f.Comments.Leading)) ||
		secretFieldMarkerRE.MatchString(string(f.Comments.Trailing))
}

// readOnlyFieldMarkerRE matches the read-only field markers
// (codegen.ReadOnlyProtoMarkers — `forge:read-only`, `forge:computed` and
// `forge:generated`)
// inside a proto leading/trailing comment. Like secretFieldMarkerRE, buf
// strips the `//` before protogen sees the text, so the token is matched
// WITHOUT the leading slashes; spacing/prose variants tolerated
// (`forge:read-only`, `  forge:read-only — the server owns this`), any line
// of a multi-line block. Marker names from the registry, as with
// secretFieldMarkerRE.
//
// forge:computed matches here too, so a computed field is excluded from the
// write envelopes by exactly the rule read-only is — the descriptor path and
// the raw scanner share the registry set precisely so a field's writability
// cannot depend on which pass found it.
var readOnlyFieldMarkerRE = codegen.ProtoMarkerAnyCommentRE(codegen.ReadOnlyProtoMarkers)

// fieldHasReadOnlyMarker reports whether a field carries a read-only marker
// (`// forge:read-only` or `// forge:computed`) as its LEADING comment (the
// documented site) or a TRAILING one (`string status = 4; // forge:read-only`).
// The marker keeps the column + read responses but excludes the field from the
// generated Create/Update request messages; see SchemaFieldDef.ReadOnly.
func fieldHasReadOnlyMarker(f *protogen.Field) bool {
	return readOnlyFieldMarkerRE.MatchString(string(f.Comments.Leading)) ||
		readOnlyFieldMarkerRE.MatchString(string(f.Comments.Trailing))
}

// fieldGeneratedExpr returns the SQL expression a `// forge:generated <expr>`
// marker carries, from the field's LEADING comment block or a TRAILING one,
// or "" when there is none. The extractor is codegen.GeneratedMarkerExpr —
// the one the raw scanner uses — so a field births the same column whichever
// pass supplied it. The read-only half of the marker is already covered by
// fieldHasReadOnlyMarker (forge:generated is in codegen.ReadOnlyProtoMarkers).
func fieldGeneratedExpr(f *protogen.Field) string {
	if expr, ok := codegen.GeneratedMarkerExpr(string(f.Comments.Leading)); ok && expr != "" {
		return expr
	}
	expr, _ := codegen.GeneratedMarkerExpr(string(f.Comments.Trailing))
	return expr
}

// serviceMarkerComments is the comment text a service-level marker may sit
// in: every comment block between the previous declaration and
// `service X {` — the detached blocks as well as the attached one. That is
// the raw scanner's placement rule for the entity markers (only comment and
// blank lines between marker and declaration) read off the descriptor, so a
// blank line under `// forge:unmanaged` cannot quietly turn it off.
func serviceMarkerComments(svc *protogen.Service) string {
	var b strings.Builder
	for _, c := range svc.Comments.LeadingDetached {
		b.WriteString(string(c))
		b.WriteString("\n")
	}
	b.WriteString(string(svc.Comments.Leading))
	return b.String()
}

// checkUnmanagedMarkers refuses a `forge:unmanaged` marker that would do
// nothing where it is written. Both cases leave a service the author meant to
// exclude fully managed, and the next generate would scaffold a stub for each
// of its rpcs:
//
//   - a near-miss spelling above a service (`forge:unmanged`);
//   - the marker on an rpc or a top-level message. It takes a whole service,
//     never part of one, because a service's handlers are one package.
func checkUnmanagedMarkers(f *protogen.File) error {
	path := f.Desc.Path()
	for _, svc := range f.Services {
		if tok := codegen.UnmanagedMarkerNearMiss(serviceMarkerComments(svc)); tok != "" {
			return fmt.Errorf("%s: service %s: %q is not a forge marker — did you mean %q? As written, forge manages %s and scaffolds a handler stub for every rpc it declares",
				path, svc.Desc.Name(), tok, codegen.ProtoMarkerUnmanaged, svc.Desc.Name())
		}
		for _, m := range svc.Methods {
			if codegen.CommentMarksUnmanaged(string(m.Comments.Leading)) || codegen.CommentMarksUnmanaged(string(m.Comments.Trailing)) {
				return fmt.Errorf("%s: rpc %s.%s carries %q, which applies to a whole service and does nothing on an rpc. Move it above `service %s {` to leave every rpc of the service to your own handlers, or delete it",
					path, svc.Desc.Name(), m.Desc.Name(), codegen.ProtoMarkerUnmanaged, svc.Desc.Name())
			}
		}
	}
	for _, msg := range f.Messages {
		if codegen.CommentMarksUnmanaged(string(msg.Comments.Leading)) {
			return fmt.Errorf("%s: message %s carries %q, which applies to a service and does nothing on a message. Move it above the `service X {` whose handlers live outside forge, or delete it",
				path, msg.Desc.Name(), codegen.ProtoMarkerUnmanaged)
		}
	}
	return nil
}

// rpcMarkerComments is the comment text an rpc-level marker may sit in: the
// detached blocks between the previous declaration and the rpc, and the
// attached one — serviceMarkerComments' placement rule. NOT the trailing
// comment: inside a service body protobuf hands a comment that directly
// follows an rpc to THAT rpc as its trailing comment even when the author
// wrote it for the next one, so a trailing marker has no single meaning and
// checkCustomMarkers refuses it instead.
func rpcMarkerComments(m *protogen.Method) string {
	var b strings.Builder
	for _, c := range m.Comments.LeadingDetached {
		b.WriteString(string(c))
		b.WriteString("\n")
	}
	b.WriteString(string(m.Comments.Leading))
	return b.String()
}

// checkCustomMarkers refuses a `forge:custom` marker whose meaning is not the
// one its author wrote, each of which leaves forge wiring generic CRUD over
// logic the author meant to keep — or marks the wrong rpc:
//
//   - a near-miss spelling on an rpc (`forge:cusotm`, `forge:custom-rpc`);
//   - the marker in an rpc's TRAILING comment. `rpc A(...); // forge:custom`
//     and a marker on the line under `rpc A` followed by a blank line both
//     reach the descriptor as A's trailing comment, and the second one was
//     written for the rpc BELOW. Nothing in the descriptor tells them apart,
//     so neither is guessed at;
//   - the marker above a service or a top-level message. It declares ONE rpc
//     custom: a whole service whose handlers live elsewhere is
//     forge:unmanaged, and an entity with no generic CRUD is every one of its
//     CRUD-named rpcs marked — the entity then does not exist.
//
// A marker protobuf drops altogether (a block under an rpc that already has a
// same-line comment, followed by a blank line) never reaches the descriptor;
// generate's raw-source cross-check (checkCustomMarkerPlacement) catches it.
func checkCustomMarkers(f *protogen.File) error {
	path := f.Desc.Path()
	for _, svc := range f.Services {
		if codegen.CommentMarksCustom(serviceMarkerComments(svc)) {
			return fmt.Errorf("%s: service %s carries %q, which declares ONE rpc custom and does nothing on a service. Put it above each rpc whose semantics are your own, or use %q to leave the whole service to your own handlers",
				path, svc.Desc.Name(), codegen.ProtoMarkerCustom, codegen.ProtoMarkerUnmanaged)
		}
		for _, m := range svc.Methods {
			trailing := string(m.Comments.Trailing)
			if tok := codegen.CustomMarkerNearMiss(rpcMarkerComments(m) + "\n" + trailing); tok != "" {
				return fmt.Errorf("%s: rpc %s.%s: %q is not a forge marker — did you mean %q? As written, forge still treats %s as CRUD when its name matches an entity, and wires generic CRUD over it",
					path, svc.Desc.Name(), m.Desc.Name(), tok, codegen.ProtoMarkerCustom, m.Desc.Name())
			}
			if codegen.CommentMarksCustom(trailing) {
				return fmt.Errorf("%s: rpc %s.%s has %q in its trailing comment (after it on the same line, or on the line below it), and protobuf reads a comment there as belonging to %s even when it was written for the rpc below. Put the marker on its own line directly above the rpc it describes, with no blank line between",
					path, svc.Desc.Name(), m.Desc.Name(), codegen.ProtoMarkerCustom, m.Desc.Name())
			}
		}
	}
	for _, msg := range f.Messages {
		if codegen.CommentMarksCustom(string(msg.Comments.Leading)) {
			return fmt.Errorf("%s: message %s carries %q, which declares ONE rpc custom and does nothing on a message. Put it above each of %s's CRUD-named rpcs (Create%s, Get%s, ...) whose semantics are your own; marked on all of them, %s is not a forge entity",
				path, msg.Desc.Name(), codegen.ProtoMarkerCustom, msg.Desc.Name(), msg.Desc.Name(), msg.Desc.Name(), msg.Desc.Name())
		}
	}
	return nil
}

// fieldGuardTargets reads the `// forge:guards <table>.<column>` targets a
// field declares, from its LEADING comment block or a TRAILING one
// (`int64 amount_cents = 2; // forge:guards invoices.amount_paid_cents`).
// Both positions, and both orders, because the marker is most naturally
// written inline beside the field it rides on.
//
// The grammar comes from codegen.GuardTargets rather than a regex spelled
// here, so this pass and the guarded-column lint check cannot disagree about
// what counts as a target — the same single-vocabulary discipline the
// read-only and secret recognizers follow. See SchemaFieldDef.Guards.
func fieldGuardTargets(f *protogen.Field) []string {
	targets := codegen.GuardTargets(string(f.Comments.Leading))
	return append(targets, codegen.GuardTargets(string(f.Comments.Trailing))...)
}

// fieldConstraintsFromDescriptor reads a field's protovalidate
// (buf.validate.field) rules off the compiled descriptor into the
// projection-ready codegen.FieldConstraints, or nil when the field carries
// none of the projected rules. The numeric bounds and string rules are
// read GENERICALLY via protoreflect over the type-specific rules submessage
// (Int64Rules/StringRules/...) so one code path covers every scalar type
// and correctly distinguishes an explicit `gte = 0` from an unset bound
// (both spell GetGte()==0 on the typed getter).
func fieldConstraintsFromDescriptor(fd protoreflect.FieldDescriptor) *codegen.FieldConstraints {
	opts := fd.Options()
	if opts == nil || !proto.HasExtension(opts, validatepb.E_Field) {
		return nil
	}
	fr, ok := proto.GetExtension(opts, validatepb.E_Field).(*validatepb.FieldRules)
	if !ok || fr == nil {
		return nil
	}
	c := &codegen.FieldConstraints{Required: fr.GetRequired()}
	m := fr.ProtoReflect()
	if od := m.Descriptor().Oneofs().ByName("type"); od != nil {
		if which := m.WhichOneof(od); which != nil && which.Kind() == protoreflect.MessageKind {
			readNamedValidateRules(m.Get(which).Message(), c)
		}
	}
	if !c.HasAny() {
		return nil
	}
	return c
}

// readNamedValidateRules pulls the projected rules (gte/gt/lte/lt on
// numeric submessages; min_len/max_len/len/pattern/email on StringRules)
// out of a type-specific rules submessage by field NAME, using presence
// (Has) so an explicit zero bound is not lost. Field names absent on a
// given submessage (e.g. pattern on Int64Rules) are simply skipped.
func readNamedValidateRules(rules protoreflect.Message, c *codegen.FieldConstraints) {
	fields := rules.Descriptor().Fields()
	numLit := func(name string) (string, bool) {
		f := fields.ByName(protoreflect.Name(name))
		if f == nil || !rules.Has(f) {
			return "", false
		}
		v := rules.Get(f)
		switch f.Kind() {
		case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind,
			protoreflect.Sint64Kind, protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
			return strconv.FormatInt(v.Int(), 10), true
		case protoreflect.Uint32Kind, protoreflect.Uint64Kind, protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
			return strconv.FormatUint(v.Uint(), 10), true
		case protoreflect.FloatKind, protoreflect.DoubleKind:
			return strconv.FormatFloat(v.Float(), 'g', -1, 64), true
		}
		return "", false
	}
	if s, ok := numLit("gte"); ok {
		c.Gte = s
	}
	if s, ok := numLit("gt"); ok {
		c.Gt = s
	}
	if s, ok := numLit("lte"); ok {
		c.Lte = s
	}
	if s, ok := numLit("lt"); ok {
		c.Lt = s
	}
	uintVal := func(name string) *uint64 {
		f := fields.ByName(protoreflect.Name(name))
		if f == nil || !rules.Has(f) {
			return nil
		}
		n := rules.Get(f).Uint()
		return &n
	}
	c.MinLen = uintVal("min_len")
	c.MaxLen = uintVal("max_len")
	if c.MinLen == nil && c.MaxLen == nil { // string.len (exact) → min == max
		if n := uintVal("len"); n != nil {
			c.MinLen, c.MaxLen = n, n
		}
	}
	if f := fields.ByName("pattern"); f != nil && rules.Has(f) {
		c.Pattern = rules.Get(f).String()
	}
	if f := fields.ByName("email"); f != nil && rules.Has(f) {
		c.Email = rules.Get(f).Bool()
	}
}

// recordEnum stores an enum's declared value names (declaration order)
// under its fully-qualified name. protojson encodes enum values as
// their names, so this list is verbatim the JSON Schema "enum" array.
// It records each value's declared wire number alongside (EnumNumbers),
// because a reserved or skipped number makes declaration order diverge
// from the number protobuf-es types the field as.
func recordEnum(sd *codegen.ServiceDef, en *protogen.Enum) {
	fq := string(en.Desc.FullName())
	if strings.HasPrefix(fq, "google.protobuf.") {
		return
	}
	if sd.Enums == nil {
		sd.Enums = make(map[string][]string)
	}
	if _, done := sd.Enums[fq]; done {
		return
	}
	vals := make([]string, 0, len(en.Values))
	nums := make([]int32, 0, len(en.Values))
	for _, v := range en.Values {
		vals = append(vals, string(v.Desc.Name()))
		nums = append(nums, int32(v.Desc.Number()))
	}
	sd.Enums[fq] = vals
	if sd.EnumNumbers == nil {
		sd.EnumNumbers = make(map[string][]int32)
	}
	sd.EnumNumbers[fq] = nums
	if sd.EnumGoRefs == nil {
		sd.EnumGoRefs = make(map[string]codegen.EnumGoRef)
	}
	sd.EnumGoRefs[fq] = codegen.EnumGoRef{ImportPath: string(en.GoIdent.GoImportPath), GoName: en.GoIdent.GoName}
}

// extractMessageFields populates the Messages map with field definitions for a message.
func extractMessageFields(messages map[string][]codegen.MessageFieldDef, msg *protogen.Message) {
	name := string(msg.Desc.Name())
	if _, exists := messages[name]; exists {
		return
	}

	var fields []codegen.MessageFieldDef
	for _, f := range msg.Fields {
		// Repeated fields are encoded as "[]<elementKind>" so downstream
		// codegen (JSON Schema mappers, frontend hooks, etc.)
		// can distinguish scalar fields from arrays without inspecting
		// cardinality separately. FRICTION (cp-forge, 2026-06-09): a
		// strict JSON-Schema validator rejected a List response
		// because the schema said `items: object` (singular message
		// type) while the wire data was `items: [array of objects]`.
		// Root cause was right here — the descriptor dropped
		// cardinality before it reached any downstream consumer.
		protoType := protoKindToString(f.Desc.Kind())
		if f.Desc.Cardinality() == protoreflect.Repeated && !f.Desc.IsMap() {
			protoType = "[]" + protoType
		}
		fd := codegen.MessageFieldDef{
			Name:       string(f.Desc.Name()),
			ProtoType:  protoType,
			IsOptional: f.Desc.HasOptionalKeyword(),
		}
		// Carry the referenced type's name for message fields. ProtoType
		// collapses these to the literal "message", which is unmatchable —
		// the CRUD shape matcher needs to know that UpdateItemRequest.item
		// is an Item, not just "a message" (F2 root cause).
		if f.Desc.Kind() == protoreflect.MessageKind && !f.Desc.IsMap() {
			fd.MessageType = string(f.Desc.Message().FullName())
		}
		fields = append(fields, fd)
	}
	messages[name] = fields
}

// noticeLegacyEntityAnnotation prints a one-line migration notice for
// messages still carrying the retired (forge.v1.entity) annotation.
// The annotation is ignored: entities are projections of the applied
// db/migrations schema now, and these projects' migrations were already
// the de-facto truth. The option DEFINITIONS remain in forge/v1/forge.proto
// (deprecated) so legacy protos keep compiling for one release.
func noticeLegacyEntityAnnotation(file *protogen.File, msg *protogen.Message) {
	opts, ok := msg.Desc.Options().(*descriptorpb.MessageOptions)
	if !ok || opts == nil {
		return
	}
	if !proto.HasExtension(opts, forgev1.E_Entity) {
		return
	}
	fmt.Fprintf(os.Stderr,
		"ℹ️  %s: message %s carries the retired (forge.v1.entity) annotation — it is now ignored.\n"+
			"   SQL is the schema: db/migrations drive the ORM/entity projections.\n"+
			"   Your migrations are already the truth; delete the annotation (and any proto/db entity files).\n",
		file.Desc.Path(), msg.Desc.Name())
}

// configMapValueType names a map field's value kind ("string", "bool"),
// or "" for a field that is not a map.
func configMapValueType(f *protogen.Field) string {
	if !f.Desc.IsMap() {
		return ""
	}
	return protoKindToString(f.Desc.MapValue().Kind())
}

// configFieldRoleString maps the ConfigFieldRole enum to the bare enum
// spelling stored on codegen.ConfigField.Role ("" for UNSPECIFIED so old
// descriptors and unannotated fields serialize identically). Config codegen
// keys semantic behavior on this string, never on the field name.
func configFieldRoleString(r forgev1.ConfigFieldRole) string {
	if r == forgev1.ConfigFieldRole_CONFIG_FIELD_ROLE_UNSPECIFIED {
		return ""
	}
	return r.String()
}

// appendConfigMessages extracts msg (and, recursively, its nested
// message declarations) into out. Nested declarations matter for
// component config blocks: `message AppConfig { message TraderConfig
// {...} TraderConfig trader = 21; }` must surface TraderConfig as its
// own ConfigMessage even though it isn't a top-level declaration.
func appendConfigMessages(out *[]codegen.ConfigMessage, msg *protogen.Message) {
	if cm, ok := extractConfigMessage(msg); ok {
		*out = append(*out, cm)
	}
	for _, nested := range msg.Messages {
		appendConfigMessages(out, nested)
	}
}

// extractConfigMessage checks if a message has any fields with config_field options
// and extracts them into a codegen.ConfigMessage.
//
// Two field shapes participate:
//
//   - scalar fields carrying the (forge.v1.config) extension — the classic
//     env_var/flag/default leaves;
//   - message-typed fields whose target message itself has config-annotated
//     fields — component config-block references (e.g. `TraderConfig trader
//     = 21;` on AppConfig). These need NO annotation of their own; the env
//     binding lives entirely on the referenced block's leaf fields. They are
//     recorded with ProtoType "message" + MessageType naming the block so
//     the config loader can emit a nested struct and wire_gen can resolve Deps
//     fields of the block type to `cfg.<Field>`.
func extractConfigMessage(msg *protogen.Message) (codegen.ConfigMessage, bool) {
	var configFields []codegen.ConfigField

	// Every field records the proto file DECLARING it, so a generate-time
	// refusal over colliding field names can name the files to go edit
	// (codegen.CheckDuplicateConfigFields). Read off the field's own parent
	// file rather than the message's: a config block composed from another
	// proto contributes its leaves here, and the file that owns the leaf is
	// the one an author has to change.
	for _, f := range msg.Fields {
		declaredIn := f.Desc.ParentFile().Path()
		// Component config-block reference: a message-typed field whose
		// target message has config-annotated fields. Repeated/map fields
		// are excluded — a config block composes exactly once per field.
		if f.Desc.Kind() == protoreflect.MessageKind && !f.Desc.IsList() && !f.Desc.IsMap() &&
			f.Message != nil && f.Message != msg && messageHasConfigFields(f.Message) {
			configFields = append(configFields, codegen.ConfigField{
				Name:        string(f.Desc.Name()),
				GoName:      f.GoName,
				ProtoType:   "message",
				MessageType: string(f.Message.Desc.Name()),
				ProtoFile:   declaredIn,
			})
			continue
		}

		opts := f.Desc.Options()
		if opts == nil {
			continue
		}

		ext := proto.GetExtension(opts, forgev1.E_Config)
		if ext == nil {
			continue
		}

		cf, ok := ext.(*forgev1.ConfigFieldOptions)
		if !ok || cf == nil {
			continue
		}

		var aliases []codegen.EnvVarAlias
		for _, a := range cf.GetEnvVarAliases() {
			aliases = append(aliases, codegen.EnvVarAlias{Name: a.GetName(), Deprecated: a.GetDeprecated(), Values: a.GetValues()})
		}
		var requiredWhen []codegen.ConfigCondition
		for _, c := range cf.GetRequiredWhen() {
			requiredWhen = append(requiredWhen, codegen.ConfigCondition{Field: c.GetField(), Equals: c.GetEquals()})
		}
		configFields = append(configFields, codegen.ConfigField{
			Name:          string(f.Desc.Name()),
			GoName:        f.GoName,
			GoType:        codegen.ProtoTypeToGoType(protoKindToString(f.Desc.Kind())),
			ProtoType:     protoKindToString(f.Desc.Kind()),
			EnvVar:        cf.GetEnvVar(),
			EnvVarAliases: aliases,
			EnvPrefix:     cf.GetEnvPrefix(),
			EnvSuffix:     cf.GetEnvSuffix(),
			MapValueType:  configMapValueType(f),
			AllowedValues: cf.GetAllowedValues(),
			Flag:          cf.GetFlag(),
			DefaultValue:  cf.GetDefaultValue(),
			Required:      cf.GetRequired(),
			Optional:      cf.GetOptional(),
			Description:   cf.GetDescription(),
			Sensitive:     cf.GetSensitive(),
			Category:      cf.GetCategory(),
			Role:          configFieldRoleString(cf.GetRole()),
			ProtoFile:     declaredIn,
			RequiredWhen:  requiredWhen,
		})
	}

	if len(configFields) == 0 {
		return codegen.ConfigMessage{}, false
	}

	protoFile, line := configMessageLocation(msg)
	binary, command, subtrees := configMessageBinary(msg)
	return codegen.ConfigMessage{
		FullName:  string(msg.Desc.FullName()),
		Name:      string(msg.Desc.Name()),
		Fields:    configFields,
		Binary:    binary,
		Command:   command,
		Subtrees:  subtrees,
		Frontend:  configMessageFrontend(msg),
		ProtoFile: protoFile,
		Line:      line,

		GoImportPath: string(msg.GoIdent.GoImportPath),
	}, true
}

// descriptorMessageOptionsField is DescriptorProto.options' field number —
// the source-path element between a message and an option set on it.
const descriptorMessageOptionsField = 7

// configMessageLocation returns the file declaring msg and the 1-based line
// of its (forge.v1.binary_config) option, falling back to the line of the
// `message` keyword when the option carries no recorded position. The
// option line is preferred because it is the one an author edits when
// generate refuses the binding. Line is 0 when the request carried no
// source info.
func configMessageLocation(msg *protogen.Message) (string, int) {
	file := msg.Desc.ParentFile()
	if file == nil {
		return "", 0
	}
	locs := file.SourceLocations()
	optPath := append(append(protoreflect.SourcePath(nil), msg.Location.Path...),
		descriptorMessageOptionsField, int32(forgev1.E_BinaryConfig.TypeDescriptor().Number()))
	if loc := locs.ByPath(optPath); loc.Path != nil {
		return file.Path(), loc.StartLine + 1
	}
	if loc := locs.ByDescriptor(msg.Desc); loc.Path != nil {
		return file.Path(), loc.StartLine + 1
	}
	return file.Path(), 0
}

// configMessageFrontend reads the message-level
// (forge.v1.frontend_config).frontend annotation — the forge.yaml frontend
// whose bundle loads this config. Empty when the message carries no
// annotation, which is every backend config and every shared block.
func configMessageFrontend(msg *protogen.Message) string {
	opts := msg.Desc.Options()
	if opts == nil {
		return ""
	}
	fc, ok := proto.GetExtension(opts, forgev1.E_FrontendConfig).(*forgev1.FrontendConfigOptions)
	if !ok || fc == nil {
		return ""
	}
	return fc.GetFrontend()
}

// configMessageBinary reads the message-level (forge.v1.binary_config)
// annotation — the cmd/<name> leaf whose process loads this config, and the
// cobra command path below it (empty for the binary's root config), and the
// subtree paths a subtree config is loaded at or below. All empty when the
// message carries no annotation, which is the common single-AppConfig
// project and also every shared block (BaseConfig) composed onto the
// per-binary configs.
func configMessageBinary(msg *protogen.Message) (string, []string, [][]string) {
	opts := msg.Desc.Options()
	if opts == nil {
		return "", nil, nil
	}
	bc, ok := proto.GetExtension(opts, forgev1.E_BinaryConfig).(*forgev1.BinaryConfigOptions)
	if !ok || bc == nil {
		return "", nil, nil
	}
	var subtrees [][]string
	for _, p := range bc.GetSubtrees() {
		subtrees = append(subtrees, p.GetPath())
	}
	return bc.GetBinary(), bc.GetCommand(), subtrees
}

// messageHasConfigFields reports whether any DIRECT field of msg
// carries the (forge.v1.config) extension — the test for "is this
// message a config block?". Deliberately non-recursive: one level of
// block nesting is the supported shape (root Config → block leaves).
func messageHasConfigFields(msg *protogen.Message) bool {
	for _, f := range msg.Fields {
		opts := f.Desc.Options()
		if opts == nil {
			continue
		}
		if proto.HasExtension(opts, forgev1.E_Config) {
			return true
		}
	}
	return false
}

// protoKindToString returns the proto type name for a protoreflect.Kind.
func protoKindToString(k protoreflect.Kind) string {
	switch k {
	case protoreflect.BoolKind:
		return "bool"
	case protoreflect.Int32Kind:
		return "int32"
	case protoreflect.Sint32Kind:
		return "sint32"
	case protoreflect.Sfixed32Kind:
		return "sfixed32"
	case protoreflect.Uint32Kind:
		return "uint32"
	case protoreflect.Fixed32Kind:
		return "fixed32"
	case protoreflect.Int64Kind:
		return "int64"
	case protoreflect.Sint64Kind:
		return "sint64"
	case protoreflect.Sfixed64Kind:
		return "sfixed64"
	case protoreflect.Uint64Kind:
		return "uint64"
	case protoreflect.Fixed64Kind:
		return "fixed64"
	case protoreflect.FloatKind:
		return "float"
	case protoreflect.DoubleKind:
		return "double"
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BytesKind:
		return "bytes"
	case protoreflect.MessageKind:
		return "message"
	case protoreflect.EnumKind:
		return "enum"
	default:
		return "string"
	}
}
