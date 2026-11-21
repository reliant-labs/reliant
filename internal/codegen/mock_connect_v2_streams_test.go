package codegen

import (
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/connectv2test"
)

// A connect-go v2 streaming handler names its messages only through the
// per-rpc stream type the plugin generates, so a mock whose every reference
// to a message package is through a stream must not import that package:
// an unused import does not compile. These services are the shapes the
// mixed ItemsService fixture cannot catch, because its unary rpcs import
// pb on their own: a service with only a bidi rpc (pb unused), and streams
// whose messages live in another proto package (the foreign import unused,
// pb still used by a response).
func TestStreamOnlyMocksConnectV2Compile(t *testing.T) {
	const sharedProto = `syntax = "proto3";
package shared.v1;
option go_package = "example.com/v2app/gen/shared/v1;sharedv1";
message Event { string id = 1; }
message Ack { string id = 1; }
`
	for _, tc := range []struct {
		name    string
		protos  map[string]string
		methods []Method
	}{
		{
			name: "bidi only",
			protos: map[string]string{"services/items/v1/items.proto": `syntax = "proto3";
package items.v1;
option go_package = "example.com/v2app/gen/services/items/v1;itemsv1";
message ChatRequest {}
message ChatResponse {}
service ItemsService { rpc Chat(stream ChatRequest) returns (stream ChatResponse); }
`},
			methods: []Method{{Name: "Chat", InputType: "ChatRequest", OutputType: "ChatResponse", ClientStreaming: true, ServerStreaming: true}},
		},
		{
			name: "streamed foreign messages",
			protos: map[string]string{
				"shared/v1/shared.proto": sharedProto,
				"services/items/v1/items.proto": `syntax = "proto3";
package items.v1;
import "shared/v1/shared.proto";
option go_package = "example.com/v2app/gen/services/items/v1;itemsv1";
message UploadResponse {}
service ItemsService {
  rpc Chat(stream shared.v1.Event) returns (stream shared.v1.Ack);
  rpc Upload(stream shared.v1.Event) returns (UploadResponse);
}
`,
			},
			methods: []Method{
				{
					Name: "Chat", InputType: "Event", OutputType: "Ack", ClientStreaming: true, ServerStreaming: true,
					InputTypeFQ: "shared.v1.Event", OutputTypeFQ: "shared.v1.Ack",
					InputProtoFile: "proto/shared/v1/shared.proto", OutputProtoFile: "proto/shared/v1/shared.proto",
				},
				{
					Name: "Upload", InputType: "Event", OutputType: "UploadResponse", ClientStreaming: true,
					InputTypeFQ: "shared.v1.Event", OutputTypeFQ: "items.v1.UploadResponse",
					InputProtoFile: "proto/shared/v1/shared.proto", OutputProtoFile: "proto/services/items/v1/items.proto",
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mod := connectv2test.NewModule(t, v2ItemsModule, tc.protos)
			svc := v2ItemsService()
			svc.Methods = tc.methods
			if _, err := GenerateMock(svc, mod.Dir, filepath.Join(mod.Dir, "internal", "handlers", "mocks"), nil); err != nil {
				t.Fatalf("GenerateMock: %v", err)
			}
			// The mock is the generated Handler, and mounts through
			// mountkit.RegistrarV2.
			mod.Write(t, "internal/check/check.go", `package check

import (
	"github.com/reliant-labs/forge/pkg/mountkit"

	"example.com/v2app/gen/services/items/v1/itemsv1connect"
	"example.com/v2app/internal/handlers/mocks"
)

var (
	_ itemsv1connect.ItemsServiceHandler = (*mocks.ItemsServiceMock)(nil)
	_ mountkit.RegistrarV2               = (*mocks.ItemsServiceMock)(nil)
)
`)
			mod.Go(t, "vet", "./internal/handlers/mocks", "./internal/check")
		})
	}
}
