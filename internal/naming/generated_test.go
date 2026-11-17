package naming

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// trickyProtoNames are proto identifiers on which a hand-rolled title-caser
// and the real generators disagree, or nearly do: a letter after a digit,
// an underscore before a digit, leading/trailing/doubled underscores,
// version segments, acronym runs, and the names that surfaced the bug
// (`alphav1connect`, and the `base64item` / `configv1item` / `oauth2item`
// entity fields of a 146-service scaffold).
var trickyProtoNames = []string{
	"base64item", "configv1item", "oauth2item", "alphav1connect",
	"Alphav1connectService", "Base64Item", "Oauth2token", "Oauth2tokenService",
	"sha256sum", "x509_cert", "ipv4_addr", "md5", "a1b2c3", "utf8_name",
	"address_line_2", "line2", "level_10_boss", "foo_1bar", "foo_1",
	"v1", "v1alpha1", "configv1", "Tier3Plan", "oAuth2", "OAuth2Token",
	"_internal", "foo__bar", "trailing_", "FOO_BAR", "HTTPStatus",
	"http_status", "user_id", "id", "x", "already_Upper", "camelCase",
	"ListBase64Items", "LLMChat", "GetOauth2token",
}

// conflictFieldNames is one message's fields in declaration order, chosen
// so protogen's per-message conflict resolution renames several of them:
// a generated method name (descriptor, string, reset), a getter of an
// earlier field (get_name after name), and a name the rename itself then
// collides with (descriptor_ after descriptor became Descriptor_).
var conflictFieldNames = []string{
	"name", "get_name", "descriptor", "descriptor_", "string", "reset",
	"proto_message", "x", "get_x", "base64item", "base64_item",
}

const fixtureGoPrefix = "example.com/fixture/gen/"

// namingFixture builds the descriptors every parity test feeds the real
// generators: each category in its own package, because messages,
// services and enums share one symbol namespace per package.
//
//   - messages: one message per tricky name, plus a nested message and
//     enum ("Outer.inner_msg", "Outer.status_kind");
//   - fields: message F<i> holding ONLY tricky field i (isolates
//     GoCamelCase from conflict resolution), plus message Conflicts;
//   - services: one service per tricky name with an RPC Ping, plus
//     service Methods with one RPC per tricky name;
//   - enums: one enum per tricky name.
func namingFixture() *pluginpb.CodeGeneratorRequest {
	str := proto.String
	file := func(name, pkg, goPkg string) *descriptorpb.FileDescriptorProto {
		return &descriptorpb.FileDescriptorProto{
			Name:    str(name),
			Package: str(pkg),
			Syntax:  str("proto3"),
			Options: &descriptorpb.FileOptions{GoPackage: str(fixtureGoPrefix + goPkg)},
		}
	}
	field := func(name string, num int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:   str(name),
			Number: proto.Int32(num),
			Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		}
	}
	// Enum values are scoped to the PACKAGE, so each enum's lone value
	// carries a caller-unique name.
	enum := func(name, value string) *descriptorpb.EnumDescriptorProto {
		return &descriptorpb.EnumDescriptorProto{
			Name:  str(name),
			Value: []*descriptorpb.EnumValueDescriptorProto{{Name: str(value), Number: proto.Int32(0)}},
		}
	}

	msgs := file("names/messages.proto", "names.messages.v1", "names/messages/v1;messagesv1")
	for _, n := range trickyProtoNames {
		msgs.MessageType = append(msgs.MessageType, &descriptorpb.DescriptorProto{Name: str(n)})
	}
	msgs.MessageType = append(msgs.MessageType, &descriptorpb.DescriptorProto{
		Name:       str("Outer"),
		NestedType: []*descriptorpb.DescriptorProto{{Name: str("inner_msg")}},
		EnumType:   []*descriptorpb.EnumDescriptorProto{enum("status_kind", "STATUS_KIND_ZERO")},
	})

	fields := file("names/fields.proto", "names.fields.v1", "names/fields/v1;fieldsv1")
	for i, n := range trickyProtoNames {
		fields.MessageType = append(fields.MessageType, &descriptorpb.DescriptorProto{
			Name:  str(fmt.Sprintf("F%d", i)),
			Field: []*descriptorpb.FieldDescriptorProto{field(n, 1)},
		})
	}
	conflicts := &descriptorpb.DescriptorProto{Name: str("Conflicts")}
	for i, n := range conflictFieldNames {
		conflicts.Field = append(conflicts.Field, field(n, int32(i+1)))
	}
	fields.MessageType = append(fields.MessageType, conflicts)

	svcs := file("names/services.proto", "names.services.v1", "names/services/v1;servicesv1")
	svcs.MessageType = []*descriptorpb.DescriptorProto{{Name: str("Msg")}}
	rpc := func(name string) *descriptorpb.MethodDescriptorProto {
		return &descriptorpb.MethodDescriptorProto{
			Name:       str(name),
			InputType:  str(".names.services.v1.Msg"),
			OutputType: str(".names.services.v1.Msg"),
		}
	}
	methods := &descriptorpb.ServiceDescriptorProto{Name: str("Methods")}
	for _, n := range trickyProtoNames {
		svcs.Service = append(svcs.Service, &descriptorpb.ServiceDescriptorProto{
			Name:   str(n),
			Method: []*descriptorpb.MethodDescriptorProto{rpc("Ping")},
		})
		methods.Method = append(methods.Method, rpc(n))
	}
	svcs.Service = append(svcs.Service, methods)

	enums := file("names/enums.proto", "names.enums.v1", "names/enums/v1;enumsv1")
	for i, n := range trickyProtoNames {
		enums.EnumType = append(enums.EnumType, enum(n, fmt.Sprintf("E%d_ZERO", i)))
	}

	files := []*descriptorpb.FileDescriptorProto{msgs, fields, svcs, enums}
	req := &pluginpb.CodeGeneratorRequest{ProtoFile: files}
	for _, f := range files {
		req.FileToGenerate = append(req.FileToGenerate, f.GetName())
	}
	return req
}

// TestGeneratedNames_MatchProtogen checks every Go-side helper against
// protogen — the library protoc-gen-go and protoc-gen-connect-go both
// name symbols with — run in-process on the fixture. Fast, so it runs in
// -short; TestGeneratedNames_MatchRealPlugins is the out-of-process twin.
func TestGeneratedNames_MatchProtogen(t *testing.T) {
	plugin, err := protogen.Options{}.New(namingFixture())
	if err != nil {
		t.Fatalf("protogen: %v", err)
	}
	checked := 0
	check := func(what, want, got string) {
		t.Helper()
		checked++
		if got != want {
			t.Errorf("%s: forge derives %q, protoc-gen-go generates %q", what, got, want)
		}
	}
	for _, f := range plugin.Files {
		pkg := string(f.Desc.Package())
		var walk func(msgs []*protogen.Message)
		walk = func(msgs []*protogen.Message) {
			for _, m := range msgs {
				check("message "+string(m.Desc.FullName()), m.GoIdent.GoName, GoTypeName(pkg, string(m.Desc.FullName())))
				var siblings []string
				for _, fl := range m.Fields {
					siblings = append(siblings, string(fl.Desc.Name()))
				}
				for _, fl := range m.Fields {
					check("field "+string(fl.Desc.FullName()), fl.GoName, GoFieldName(string(fl.Desc.Name()), siblings))
					if len(m.Fields) == 1 {
						check("field (alone) "+string(fl.Desc.FullName()), fl.GoName, GoCamelCase(string(fl.Desc.Name())))
					}
				}
				for _, e := range m.Enums {
					check("nested enum "+string(e.Desc.FullName()), e.GoIdent.GoName, GoTypeName(pkg, string(e.Desc.FullName())))
				}
				walk(m.Messages)
			}
		}
		walk(f.Messages)
		for _, e := range f.Enums {
			check("enum "+string(e.Desc.FullName()), e.GoIdent.GoName, GoTypeName(pkg, string(e.Desc.FullName())))
		}
		for _, s := range f.Services {
			check("service "+string(s.Desc.FullName()), s.GoName, GoCamelCase(string(s.Desc.Name())))
			for _, m := range s.Methods {
				check("method "+string(m.Desc.FullName()), m.GoName, GoCamelCase(string(m.Desc.Name())))
			}
		}
	}
	if min := 5 * len(trickyProtoNames); checked < min {
		t.Fatalf("checked only %d names (want >= %d) — the fixture did not reach protogen", checked, min)
	}
}

// TestGoFieldNames_ConflictResolution pins the renames protogen makes
// within one message, so a reader can see the rule without running
// protogen. TestGeneratedNames_MatchProtogen proves the same list against
// protogen itself.
func TestGoFieldNames_ConflictResolution(t *testing.T) {
	want := []string{
		"Name", "GetName_", "Descriptor_", "Descriptor__", "String_", "Reset_",
		"ProtoMessage_", "X", "GetX_", "Base64Item", "Base64Item_",
	}
	got := GoFieldNames(conflictFieldNames)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("field %q: got %q, want %q", conflictFieldNames[i], got[i], want[i])
		}
	}
}

// TestGeneratedNames_MatchRealPlugins builds protoc-gen-go and
// protoc-gen-connect-go at the versions go.mod pins, runs them on the
// fixture exactly as buf would (CodeGeneratorRequest on stdin), and checks
// that every identifier forge derives is DECLARED in what they wrote.
func TestGeneratedNames_MatchRealPlugins(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs protoc-gen-go and protoc-gen-connect-go; TestGeneratedNames_MatchProtogen covers -short")
	}
	bin := t.TempDir()
	build := func(pkg string) string {
		out := filepath.Join(bin, path.Base(pkg))
		if b, err := exec.Command("go", "build", "-o", out, pkg).CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", pkg, err, b)
		}
		return out
	}
	req := namingFixture()
	goDecls := runPlugin(t, build("google.golang.org/protobuf/cmd/protoc-gen-go"), req)
	connectDecls := runPlugin(t, build("connectrpc.com/connect/cmd/protoc-gen-connect-go"), req)

	has := func(decls map[string]map[string]bool, file, ident string) bool {
		return decls[file][ident]
	}
	byName := map[string]*descriptorpb.FileDescriptorProto{}
	for _, f := range req.ProtoFile {
		byName[f.GetName()] = f
	}

	// protoc-gen-go: message types and their struct fields.
	for _, fname := range []string{"names/messages.proto", "names/fields.proto"} {
		f := byName[fname]
		dir, _ := goPackage(f)
		out := dir + "/" + strings.TrimSuffix(path.Base(fname), ".proto") + ".pb.go"
		if _, ok := goDecls[out]; !ok {
			t.Fatalf("protoc-gen-go wrote no %s (wrote %v)", out, keys(goDecls))
		}
		for _, m := range f.MessageType {
			typ := GoTypeName(f.GetPackage(), f.GetPackage()+"."+m.GetName())
			if !has(goDecls, out, typ) {
				t.Errorf("protoc-gen-go declares no type %s (from message %s) in %s", typ, m.GetName(), out)
			}
			var siblings []string
			for _, fl := range m.Field {
				siblings = append(siblings, fl.GetName())
			}
			for _, fl := range m.Field {
				goField := typ + "." + GoFieldName(fl.GetName(), siblings)
				if !has(goDecls, out, goField) {
					t.Errorf("protoc-gen-go declares no field %s (from %s.%s)", goField, m.GetName(), fl.GetName())
				}
			}
			for _, n := range m.NestedType {
				nested := GoTypeName(f.GetPackage(), f.GetPackage()+"."+m.GetName()+"."+n.GetName())
				if !has(goDecls, out, nested) {
					t.Errorf("protoc-gen-go declares no nested type %s", nested)
				}
			}
		}
	}

	// protoc-gen-connect-go: the package, and every identifier per
	// service and per RPC.
	svcs := byName["names/services.proto"]
	dir, pkgName := goPackage(svcs)
	out := dir + "/" + ConnectPackage(pkgName) + "/services.connect.go"
	if _, ok := connectDecls[out]; !ok {
		t.Fatalf("protoc-gen-connect-go wrote no %s (wrote %v)", out, keys(connectDecls))
	}
	for _, s := range svcs.Service {
		base := ConnectServiceGoName(s.GetName())
		for _, ident := range []string{
			base + "Handler",
			"Unimplemented" + base + "Handler",
			"New" + base + "Handler",
			base + "Client",
			"New" + base + "Client",
			ConnectServiceNameConst(s.GetName()),
		} {
			if !has(connectDecls, out, ident) {
				t.Errorf("protoc-gen-connect-go declares no %s (service %s)", ident, s.GetName())
			}
		}
		for _, m := range s.Method {
			if c := ConnectProcedureConst(s.GetName(), m.GetName()); !has(connectDecls, out, c) {
				t.Errorf("protoc-gen-connect-go declares no %s (rpc %s.%s)", c, s.GetName(), m.GetName())
			}
			if h := base + "Handler." + GoCamelCase(m.GetName()); !has(connectDecls, out, h) {
				t.Errorf("protoc-gen-connect-go's handler interface has no method %s (rpc %s.%s)", h, s.GetName(), m.GetName())
			}
		}
	}
}

// goPackage splits a fixture file's go_package option into the import
// path protoc-gen-go writes under and the Go package name:
// ("example.com/fixture/gen/names/services/v1", "servicesv1").
func goPackage(f *descriptorpb.FileDescriptorProto) (importPath, name string) {
	importPath, name, _ = strings.Cut(f.GetOptions().GetGoPackage(), ";")
	return importPath, name
}

// runPlugin runs a protoc plugin binary on req and returns, per generated
// file, every identifier it declares at package level plus "Type.Member"
// for struct fields and interface methods.
func runPlugin(t *testing.T, bin string, req *pluginpb.CodeGeneratorRequest) map[string]map[string]bool {
	t.Helper()
	in, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Stdin = bytes.NewReader(in)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v\n%s", bin, err, stderr.String())
	}
	var resp pluginpb.CodeGeneratorResponse
	if err := proto.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("%s: %s", bin, resp.GetError())
	}
	decls := map[string]map[string]bool{}
	for _, gf := range resp.File {
		parsed, err := parser.ParseFile(token.NewFileSet(), gf.GetName(), gf.GetContent(), 0)
		if err != nil {
			t.Fatalf("parse %s: %v", gf.GetName(), err)
		}
		set := map[string]bool{}
		for _, d := range parsed.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					set[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						set[s.Name.Name] = true
						var members *ast.FieldList
						switch tt := s.Type.(type) {
						case *ast.StructType:
							members = tt.Fields
						case *ast.InterfaceType:
							members = tt.Methods
						}
						if members != nil {
							for _, fl := range members.List {
								for _, n := range fl.Names {
									set[s.Name.Name+"."+n.Name] = true
								}
							}
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							set[n.Name] = true
						}
					}
				}
			}
		}
		decls[gf.GetName()] = set
	}
	return decls
}

func keys(m map[string]map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestEsNames_MatchProtobufEs pins the protobuf-es (protoc-gen-es v2)
// localNames forge references from generated TypeScript. The expected
// values were produced by running @bufbuild/protoc-gen-es 2.10.1 over the
// same names (field `item.<x>`, client method `client.<x>`); the rules are
// protoCamelCase + safeObjectProperty for fields and
// lowerFirst + safeObjectProperty for methods (@bufbuild/protobuf
// reflect/names.ts and registry.ts).
func TestEsNames_MatchProtobufEs(t *testing.T) {
	fields := map[string]string{
		"base64item":     "base64item",
		"configv1item":   "configv1item",
		"address_line_2": "addressLine2",
		"level_10_boss":  "level10Boss",
		"foo_1bar":       "foo1bar",
		"x509_cert":      "x509Cert",
		"_internal":      "Internal",
		"foo__bar":       "fooBar",
		"trailing_":      "trailing",
		"FOO_BAR":        "FOOBAR",
		"user_id":        "userId",
		"already_Upper":  "alreadyUpper",
		"camelCase":      "camelCase",
		"constructor":    "constructor$",
		"to_string":      "toString$",
		"value_of":       "valueOf$",
		"to_j_s_o_n":     "toJSON$",
	}
	for in, want := range fields {
		if got := EsFieldName(in); got != want {
			t.Errorf("EsFieldName(%q) = %q, protoc-gen-es generates %q", in, got, want)
		}
	}
	methods := map[string]string{
		"ListBase64Items": "listBase64Items",
		"GetOauth2token":  "getOauth2token",
		"LLMChat":         "lLMChat",
		"HTTPGet":         "hTTPGet",
		"Ping":            "ping",
		"Constructor":     "constructor$",
		"ToString":        "toString$",
		"_internal":       "_internal",
	}
	for in, want := range methods {
		if got := EsMethodName(in); got != want {
			t.Errorf("EsMethodName(%q) = %q, protoc-gen-es generates %q", in, got, want)
		}
	}
}
