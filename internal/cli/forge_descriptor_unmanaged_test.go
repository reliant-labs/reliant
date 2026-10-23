package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
)

// unmanagedComment is one comment attached by SourceCodeInfo path. buf hands
// protogen the text with the `//` already stripped, which is the shape these
// carry. Paths: 4 = message_type, 6 = service, 2 = method (within a service).
type unmanagedComment struct {
	path     []int32
	leading  string
	trailing string
	detached []string
}

// buildUnmanagedPlugin compiles chat.v1 with two services (ChatService,
// BillingService, one rpc each) and one top-level message, with whatever
// comments the case attaches.
func buildUnmanagedPlugin(t *testing.T, comments ...unmanagedComment) *protogen.File {
	t.Helper()
	empty := &descriptorpb.DescriptorProto{Name: proto.String("Empty")}
	svc := func(name, rpc string) *descriptorpb.ServiceDescriptorProto {
		return &descriptorpb.ServiceDescriptorProto{
			Name: proto.String(name),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String(rpc),
				InputType:  proto.String(".chat.v1.Empty"),
				OutputType: proto.String(".chat.v1.Empty"),
			}},
		}
	}
	var locs []*descriptorpb.SourceCodeInfo_Location
	for _, c := range comments {
		l := &descriptorpb.SourceCodeInfo_Location{Path: c.path, Span: []int32{1, 0, 1}}
		if c.leading != "" {
			l.LeadingComments = proto.String(c.leading)
		}
		if c.trailing != "" {
			l.TrailingComments = proto.String(c.trailing)
		}
		l.LeadingDetachedComments = c.detached
		locs = append(locs, l)
	}
	file := &descriptorpb.FileDescriptorProto{
		Name:           proto.String("chat/v1/chat.proto"),
		Package:        proto.String("chat.v1"),
		Syntax:         proto.String("proto3"),
		Options:        &descriptorpb.FileOptions{GoPackage: proto.String("example.com/gen/chat/v1;chatv1")},
		MessageType:    []*descriptorpb.DescriptorProto{empty},
		Service:        []*descriptorpb.ServiceDescriptorProto{svc("ChatService", "SendMessage"), svc("BillingService", "GetInvoice")},
		SourceCodeInfo: &descriptorpb.SourceCodeInfo{Location: locs},
	}
	p, err := protogen.Options{}.New(&pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{file.GetName()},
		ProtoFile:      []*descriptorpb.FileDescriptorProto{file},
	})
	if err != nil {
		t.Fatalf("protogen.New: %v", err)
	}
	for _, f := range p.Files {
		if f.Generate {
			return f
		}
	}
	t.Fatal("no generated file in plugin")
	return nil
}

// The descriptor is the one place every service consumer learns a service is
// unmanaged, so the marker must survive the protogen round trip — from the
// attached comment AND from a detached block above it (a blank line under the
// marker must not quietly turn it off) — and touch only its own service.
func TestExtractService_UnmanagedMarker(t *testing.T) {
	cases := []struct {
		name    string
		comment unmanagedComment
	}{
		{"leading comment", unmanagedComment{path: []int32{6, 0}, leading: " forge:unmanaged\n"}},
		{"leading with prose", unmanagedComment{path: []int32{6, 0}, leading: " ChatService streams messages.\n forge:unmanaged — still in internal/grpc\n"}},
		{"detached block above", unmanagedComment{path: []int32{6, 0}, detached: []string{" forge:unmanaged\n"}, leading: " ChatService streams messages.\n"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := buildUnmanagedPlugin(t, c.comment)
			if err := checkUnmanagedMarkers(f); err != nil {
				t.Fatalf("checkUnmanagedMarkers: %v", err)
			}
			chat := extractService(f, f.Services[0])
			billing := extractService(f, f.Services[1])
			if !chat.Unmanaged {
				t.Error("ChatService: forge:unmanaged not read off the descriptor")
			}
			if billing.Unmanaged {
				t.Error("BillingService: carries no marker but came out unmanaged")
			}
		})
	}

	t.Run("prose mention is not the marker", func(t *testing.T) {
		f := buildUnmanagedPlugin(t, unmanagedComment{path: []int32{6, 0}, leading: " Unlike the legacy services (see forge:unmanaged), this one is ours.\n"})
		if extractService(f, f.Services[0]).Unmanaged {
			t.Error("a mid-sentence mention marked the service unmanaged")
		}
	})
}

// A marker that does nothing where it is written leaves a service the author
// meant to exclude fully managed, and generate scaffolds a stub for each of
// its rpcs. Descriptor extraction refuses each such spelling, naming the fix.
func TestCheckUnmanagedMarkers_RefusesMarkersThatWouldDoNothing(t *testing.T) {
	cases := []struct {
		name    string
		comment unmanagedComment
		want    []string
	}{
		{
			name:    "near-miss spelling on a service",
			comment: unmanagedComment{path: []int32{6, 0}, leading: " forge:unmanged\n"},
			want:    []string{"chat/v1/chat.proto", "ChatService", `"forge:unmanged"`, `did you mean "forge:unmanaged"`},
		},
		{
			name:    "on an rpc (leading)",
			comment: unmanagedComment{path: []int32{6, 1, 2, 0}, leading: " forge:unmanaged\n"},
			want:    []string{"rpc BillingService.GetInvoice", "whole service", "service BillingService {"},
		},
		{
			name:    "on an rpc (trailing)",
			comment: unmanagedComment{path: []int32{6, 0, 2, 0}, trailing: " forge:unmanaged\n"},
			want:    []string{"rpc ChatService.SendMessage"},
		},
		{
			name:    "on a message",
			comment: unmanagedComment{path: []int32{4, 0}, leading: " forge:unmanaged\n"},
			want:    []string{"message Empty", "does nothing on a message"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkUnmanagedMarkers(buildUnmanagedPlugin(t, c.comment))
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}

	t.Run("other markers and prose pass", func(t *testing.T) {
		f := buildUnmanagedPlugin(t,
			unmanagedComment{path: []int32{6, 0}, leading: " forge:unmanaged\n"},
			unmanagedComment{path: []int32{6, 1, 2, 0}, leading: " forge:mutation\n"},
			unmanagedComment{path: []int32{6, 1}, leading: " Billing. See forge:entity for the tables.\n"},
		)
		if err := checkUnmanagedMarkers(f); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})
}

// generateDescriptor is where the refusal has to fire: descriptor extraction
// is a hard gate in the pipeline, so failing here stops generate before any
// emitter reads a descriptor that silently kept the service managed.
func TestGenerateDescriptor_FailsOnMisplacedUnmanagedMarker(t *testing.T) {
	f := buildUnmanagedPlugin(t, unmanagedComment{path: []int32{6, 0, 2, 0}, leading: " forge:unmanaged\n"})
	p := &protogen.Plugin{Files: []*protogen.File{f}}
	out := filepath.Join(t.TempDir(), "gen")
	err := generateDescriptor(p, out)
	if err == nil || !strings.Contains(err.Error(), "forge:unmanaged") {
		t.Fatalf("generateDescriptor error = %v, want a forge:unmanaged placement refusal", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(out, descriptorStageDir)); len(entries) != 0 {
		t.Errorf("a refused extraction still wrote %d fragment(s)", len(entries))
	}
}

// stepParseServicesAndModule is where the pipeline splits the two sets: every
// service-scoped emitter reads ctx.Services, so an unmanaged service must be
// absent from it, while ctx.DeclaredServices keeps the whole contract.
func TestStepParseServices_SplitsManagedFromDeclared(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDesc := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "gen", "forge_descriptor.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeDesc(`{"services":[{"Name":"ChatService","Package":"chat.v1","Unmanaged":true},{"Name":"BillingService","Package":"chat.v1"}]}`)
	ctx := &pipelineContext{ProjectDir: dir, HasServices: true}
	if err := stepParseServicesAndModule(ctx); err != nil {
		t.Fatalf("stepParseServicesAndModule: %v", err)
	}
	if len(ctx.Services) != 1 || ctx.Services[0].Name != "BillingService" {
		t.Errorf("ctx.Services = %+v, want only BillingService", ctx.Services)
	}
	if len(ctx.DeclaredServices) != 2 {
		t.Errorf("ctx.DeclaredServices = %+v, want both services", ctx.DeclaredServices)
	}
	if ctx.ModulePath != "example.com/app" {
		t.Errorf("ModulePath = %q, want example.com/app", ctx.ModulePath)
	}

	// All unmanaged: the project still DECLARES services (the gates stay
	// open for the open-procedure set), and the module path still resolves.
	writeDesc(`{"services":[{"Name":"ChatService","Package":"chat.v1","Unmanaged":true}]}`)
	if !projectDefinesConnectServices(dir) {
		t.Error("a project whose only service is unmanaged still declares services")
	}
	ctx = &pipelineContext{ProjectDir: dir, HasServices: true}
	if err := stepParseServicesAndModule(ctx); err != nil {
		t.Fatalf("stepParseServicesAndModule (all unmanaged): %v", err)
	}
	if len(ctx.Services) != 0 || len(ctx.DeclaredServices) != 1 || ctx.ModulePath != "example.com/app" {
		t.Errorf("all-unmanaged ctx: services=%d declared=%d module=%q, want 0, 1, example.com/app",
			len(ctx.Services), len(ctx.DeclaredServices), ctx.ModulePath)
	}
}

// The open-procedure set reads DeclaredServices: whether an rpc is public is
// part of the contract, true whoever implements the handler, and a
// hand-mounted unmanaged handler behind forge's interceptor must not fail
// closed. Every other emitter reads Services, which excludes it.
func TestStepAuthProcedures_CoversUnmanagedServices(t *testing.T) {
	dir := t.TempDir()
	open := codegen.Method{Name: "GetStatus", AuthRequired: false}
	closed := codegen.Method{Name: "SendMessage", AuthRequired: true}
	unmanaged := codegen.ServiceDef{
		Name: "ChatService", Package: "chat.v1", PkgName: "chatv1",
		GoPackage: "example.com/app/gen/chat/v1", Methods: []codegen.Method{open, closed}, Unmanaged: true,
	}
	ctx := &pipelineContext{
		ProjectDir:       dir,
		ModulePath:       "example.com/app",
		Checksums:        &checksums.FileChecksums{},
		Services:         nil, // nothing managed
		DeclaredServices: []codegen.ServiceDef{unmanaged},
	}
	if err := stepAuthProcedures(ctx); err != nil {
		t.Fatalf("stepAuthProcedures: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "pkg", "middleware", "procedures_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "ChatServiceGetStatusProcedure") {
		t.Errorf("unmanaged service's public rpc missing from the open set:\n%s", got)
	}
	if strings.Contains(string(got), "ChatServiceSendMessageProcedure") {
		t.Errorf("an auth-required rpc landed in the open set:\n%s", got)
	}
}
