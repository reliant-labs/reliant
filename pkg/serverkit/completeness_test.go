package serverkit_test

import (
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// buildServiceFiles compiles a single synthetic proto file declaring the
// named services (one trivial method each) and returns it as a SCOPED
// *protoregistry.Files. Building real descriptors (vs. hand-stuffing a
// fake) exercises the exact descriptor-walk RequireMounted relies on, and
// keeping the registry scoped (not GlobalFiles) makes the test hermetic —
// it never depends on what else the test binary happens to link.
func buildServiceFiles(t *testing.T, pkgName string, services ...string) *protoregistry.Files {
	t.Helper()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("serverkit_testsvc.proto"),
		Package: proto.String(pkgName),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Empty")},
		},
	}
	for _, svc := range services {
		fdp.Service = append(fdp.Service, &descriptorpb.ServiceDescriptorProto{
			Name: proto.String(svc),
			Method: []*descriptorpb.MethodDescriptorProto{
				{
					Name:       proto.String("Do"),
					InputType:  proto.String("." + pkgName + ".Empty"),
					OutputType: proto.String("." + pkgName + ".Empty"),
				},
			},
		})
	}

	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	files := &protoregistry.Files{}
	if err := files.RegisterFile(fd); err != nil {
		t.Fatalf("RegisterFile: %v", err)
	}
	return files
}

func TestRequireMounted_AllMounted(t *testing.T) {
	files := buildServiceFiles(t, "serverkit.complete.v1", "Alpha", "Beta", "Gamma")

	var srv serverkit.Server
	srv.Mounted("serverkit.complete.v1.Alpha")
	srv.Mounted("serverkit.complete.v1.Beta")
	srv.Mounted("serverkit.complete.v1.Gamma")

	if err := srv.RequireMounted(files); err != nil {
		t.Fatalf("all services mounted: want nil, got %v", err)
	}
	// Package-level form is identical.
	if err := serverkit.RequireMounted(&srv, files); err != nil {
		t.Fatalf("package-level RequireMounted: want nil, got %v", err)
	}
}

func TestRequireMounted_MissingNamed(t *testing.T) {
	files := buildServiceFiles(t, "serverkit.missing.v1", "Alpha", "Beta", "Gamma")

	// Mount only 2 of the 3 declared services.
	var srv serverkit.Server
	srv.Mounted("serverkit.missing.v1.Alpha")
	srv.Mounted("serverkit.missing.v1.Gamma")

	err := srv.RequireMounted(files)
	if err == nil {
		t.Fatal("two of three mounted: want error naming the missing service, got nil")
	}
	// The error must name the unmounted one and NOT the mounted ones.
	if !strings.Contains(err.Error(), "serverkit.missing.v1.Beta") {
		t.Errorf("error must name missing service Beta; got: %v", err)
	}
	if strings.Contains(err.Error(), "serverkit.missing.v1.Alpha") ||
		strings.Contains(err.Error(), "serverkit.missing.v1.Gamma") {
		t.Errorf("error must not name mounted services Alpha/Gamma; got: %v", err)
	}
}

func TestRequireMounted_NoServicesDeclared(t *testing.T) {
	// A file with zero services → trivially complete.
	files := buildServiceFiles(t, "serverkit.none.v1")
	var srv serverkit.Server
	if err := srv.RequireMounted(files); err != nil {
		t.Fatalf("no declared services: want nil, got %v", err)
	}
}

func TestRequireMounted_NilSource(t *testing.T) {
	var srv serverkit.Server
	if err := srv.RequireMounted(nil); err == nil {
		t.Fatal("nil descriptor source: want error, got nil")
	}
}

// recordingService implements mountkit.Registrar so Server.Mount can both
// register it and record its name.
type recordingService struct {
	path       string
	registered bool
	gotOpts    []connect.HandlerOption
}

func (r *recordingService) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	r.registered = true
	r.gotOpts = opts
	mux.HandleFunc("/"+r.path+"/Do", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestServerMount_RegistersAndRecords(t *testing.T) {
	files := buildServiceFiles(t, "serverkit.mount.v1", "Alpha")

	opt := connect.WithInterceptors() // a distinguishable (empty) handler option
	srv := serverkit.Server{
		Mux:         http.NewServeMux(),
		HandlerOpts: []connect.HandlerOption{opt},
	}

	svc := &recordingService{path: "serverkit.mount.v1.Alpha"}
	srv.Mount("serverkit.mount.v1.Alpha", svc)

	if !svc.registered {
		t.Fatal("Mount did not call Register on the service")
	}
	if len(svc.gotOpts) != 1 {
		t.Errorf("Mount did not thread Server.HandlerOpts through to Register; got %d opts", len(svc.gotOpts))
	}
	// Recording happened → completeness check passes.
	if err := srv.RequireMounted(files); err != nil {
		t.Errorf("after Mount, RequireMounted should pass; got %v", err)
	}
	if got := srv.MountedNames(); len(got) != 1 || got[0] != "serverkit.mount.v1.Alpha" {
		t.Errorf("MountedNames = %v, want [serverkit.mount.v1.Alpha]", got)
	}
}

func TestServerMount_NilMuxPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Mount with nil Mux: want panic, got none")
		}
	}()
	var srv serverkit.Server // Mux is nil
	srv.Mount("serverkit.x.v1.Alpha", &recordingService{path: "x"})
}

func TestAddWorkerAddOperator(t *testing.T) {
	var srv serverkit.Server
	srv.AddWorker(nil) // ignored
	srv.AddOperator(nil)
	if len(srv.Workers) != 0 || len(srv.Operators) != 0 {
		t.Fatal("nil worker/operator should be ignored")
	}
	// stubWorker / stubOperator are defined in serverkit_test.go /
	// failure_policy_test.go (same test package).
	srv.AddWorker(&stubWorker{name: "w1"})
	srv.AddOperator(&stubOperator{name: "o1"})
	if len(srv.Workers) != 1 || srv.Workers[0].Name() != "w1" {
		t.Errorf("AddWorker did not append; got %v", srv.Workers)
	}
	if len(srv.Operators) != 1 || srv.Operators[0].Name() != "o1" {
		t.Errorf("AddOperator did not append; got %v", srv.Operators)
	}
}
