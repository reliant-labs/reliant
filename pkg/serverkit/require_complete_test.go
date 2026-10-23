package serverkit_test

import (
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/reliant-labs/forge/pkg/mountkit/inventory"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

var registerRequireCompleteFile sync.Once

// registerTwoServiceFile puts ONE proto file declaring two services into
// protoregistry.GlobalFiles — the registry RequireComplete resolves against.
// The package name is unique to this test so nothing else the binary links
// can collide with it; sync.Once because GlobalFiles refuses a second
// registration of the same file.
func registerTwoServiceFile(t *testing.T) {
	t.Helper()
	registerRequireCompleteFile.Do(func() {
		const pkg = "serverkit.requirecomplete.v1"
		svc := func(name string) *descriptorpb.ServiceDescriptorProto {
			return &descriptorpb.ServiceDescriptorProto{
				Name: proto.String(name),
				Method: []*descriptorpb.MethodDescriptorProto{{
					Name:       proto.String("Do"),
					InputType:  proto.String("." + pkg + ".Empty"),
					OutputType: proto.String("." + pkg + ".Empty"),
				}},
			}
		}
		fdp := &descriptorpb.FileDescriptorProto{
			Name:        proto.String("serverkit_requirecomplete.proto"),
			Package:     proto.String(pkg),
			Syntax:      proto.String("proto3"),
			MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Empty")}},
			Service:     []*descriptorpb.ServiceDescriptorProto{svc("ManagedService"), svc("UnmanagedService")},
		}
		fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
		if err != nil {
			t.Fatalf("protodesc.NewFile: %v", err)
		}
		if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
			t.Fatalf("RegisterFile: %v", err)
		}
	})
}

// A proto file can declare a service forge manages beside one it does not
// (`// forge:unmanaged` — its handlers live elsewhere, so it is not in the
// generated inventory). The gate's scope is EXACTLY the inventory: widening it
// to every service in the inventory's proto files would refuse to boot a
// complete server over a service that was never forge's to mount.
func TestRequireComplete_ScopedToInventoryNotWholeFile(t *testing.T) {
	registerTwoServiceFile(t)
	inv := []inventory.ComponentInfo{{
		Name:        "managed",
		ConnectPath: "serverkit.requirecomplete.v1.ManagedService",
		Kind:        "service",
	}}

	t.Run("inventory mounted boots, the unmanaged sibling is not demanded", func(t *testing.T) {
		var srv serverkit.Server
		srv.Mounted("serverkit.requirecomplete.v1.ManagedService")
		if err := srv.RequireComplete(inv); err != nil {
			t.Fatalf("RequireComplete: want nil, got %v", err)
		}
	})

	t.Run("an unmounted inventory service still fails boot, named", func(t *testing.T) {
		var srv serverkit.Server
		err := srv.RequireComplete(inv)
		if err == nil {
			t.Fatal("RequireComplete: want an error for the unmounted inventory service")
		}
		if !strings.Contains(err.Error(), "serverkit.requirecomplete.v1.ManagedService") {
			t.Errorf("error does not name the unmounted service: %v", err)
		}
		if strings.Contains(err.Error(), "UnmanagedService") {
			t.Errorf("error demands the service outside the inventory: %v", err)
		}
	})

	t.Run("an inventory service the binary never linked is a hard error", func(t *testing.T) {
		var srv serverkit.Server
		srv.Mounted("serverkit.requirecomplete.v1.GhostService")
		err := srv.RequireComplete([]inventory.ComponentInfo{{
			Name: "ghost", ConnectPath: "serverkit.requirecomplete.v1.GhostService",
		}})
		if err == nil || !strings.Contains(err.Error(), "not found in global proto registry") {
			t.Fatalf("RequireComplete: want a not-in-registry error, got %v", err)
		}
	})

	t.Run("an inventory path naming a non-service is a hard error", func(t *testing.T) {
		var srv serverkit.Server
		srv.Mounted("serverkit.requirecomplete.v1.Empty")
		err := srv.RequireComplete([]inventory.ComponentInfo{{
			Name: "empty", ConnectPath: "serverkit.requirecomplete.v1.Empty",
		}})
		if err == nil || !strings.Contains(err.Error(), "not a service") {
			t.Fatalf("RequireComplete: want a not-a-service error, got %v", err)
		}
	})

	t.Run("empty inventory is trivially complete", func(t *testing.T) {
		var srv serverkit.Server
		if err := srv.RequireComplete(nil); err != nil {
			t.Fatalf("RequireComplete(nil): want nil, got %v", err)
		}
	})
}
