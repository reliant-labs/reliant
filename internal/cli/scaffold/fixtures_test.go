// File: internal/cli/scaffold/fixtures_test.go
//
// Shared test fixtures for the `forge scaffold rpc` / `forge project
// scaffold` unit tests: a TasksService handler project (service.go +
// handlers.go with pb-through unwired stubs) plus a matching descriptor.
// These fixtures were factored out of the (deleted) typed-vertical test
// file when the two RPC codegen architectures were collapsed into the
// single pb-through shape.

package scaffold

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// verticalDescriptor builds the gen/forge_descriptor.json content for the
// fixture: TasksService with a unary SubmitOrder (Timestamp / optional /
// repeated / nested-message / enum / map fields, auth_required) and a
// server-streaming TailEvents.
func verticalDescriptor() codegen.ForgeDescriptor {
	const pkg = "services.tasks.v1"
	sd := codegen.ServiceDef{
		Name:      "TasksService",
		Package:   pkg,
		GoPackage: "example.com/testproj/gen/services/tasks/v1",
		PkgName:   "tasksv1",
		Methods: []codegen.Method{
			{
				Name:         "SubmitOrder",
				InputType:    "SubmitOrderRequest",
				OutputType:   "SubmitOrderResponse",
				InputTypeFQ:  pkg + ".SubmitOrderRequest",
				OutputTypeFQ: pkg + ".SubmitOrderResponse",
				AuthRequired: true,
			},
			{
				Name:            "TailEvents",
				InputType:       "TailEventsRequest",
				OutputType:      "TailEventsResponse",
				InputTypeFQ:     pkg + ".TailEventsRequest",
				OutputTypeFQ:    pkg + ".TailEventsResponse",
				ServerStreaming: true,
				AuthRequired:    true,
			},
		},
		Schemas: map[string][]codegen.SchemaFieldDef{
			pkg + ".SubmitOrderRequest": {
				{Name: "customer_id", Kind: "string"},
				{Name: "note", Kind: "string", Optional: true},
				{Name: "when", Kind: "message", TypeName: "google.protobuf.Timestamp"},
				{Name: "tags", Kind: "string", Repeated: true},
				{Name: "order", Kind: "message", TypeName: pkg + ".Order"},
				{Name: "status", Kind: "enum", TypeName: pkg + ".OrderStatus"},
				{Name: "attrs", Kind: "map", MapKeyKind: "string", MapValueKind: "int64"},
			},
			pkg + ".SubmitOrderResponse": {
				{Name: "id", Kind: "string"},
				{Name: "created_at", Kind: "message", TypeName: "google.protobuf.Timestamp"},
				{Name: "items", Kind: "message", TypeName: pkg + ".Order", Repeated: true},
			},
			pkg + ".Order": {
				{Name: "sku", Kind: "string"},
				{Name: "quantity", Kind: "int64"},
				{Name: "customer_id", Kind: "string"},
			},
			pkg + ".TailEventsRequest":  {{Name: "cursor", Kind: "string"}},
			pkg + ".TailEventsResponse": {{Name: "event", Kind: "string"}},
		},
		Enums: map[string][]string{
			pkg + ".OrderStatus": {"ORDER_STATUS_UNSPECIFIED", "ORDER_STATUS_OPEN"},
		},
	}
	return codegen.ForgeDescriptor{Services: []codegen.ServiceDef{sd}}
}

func writeFixtureFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const fixtureHandlerServiceGo = `package tasks

import (
	"fmt"
	"log/slog"
)

// Deps holds dependencies for the tasks service.
type Deps struct {
	Logger *slog.Logger
	// Add your dependencies here (e.g. Repo Repository).
}

func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("tasks: Deps.Logger is required")
	}
	// Add checks for your required Deps fields here. Example:
	//   if d.Repo == nil { return fmt.Errorf("tasks: Deps.Repo is required") }
	return nil
}

// Service implements the tasks Connect RPC service.
type Service struct {
	deps Deps
}

// New creates a new tasks service instance.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}
`

const fixtureHandlersGo = `package tasks

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	pb "example.com/testproj/gen/services/tasks/v1"
)

// SubmitOrder implements the SubmitOrder RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=tasks.SubmitOrder
func (s *Service) SubmitOrder(
	ctx context.Context,
	req *connect.Request[pb.SubmitOrderRequest],
) (*connect.Response[pb.SubmitOrderResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("handler for %s not yet implemented", "SubmitOrder"))
}

// TailEvents implements the TailEvents server streaming RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=tasks.TailEvents
func (s *Service) TailEvents(
	ctx context.Context,
	req *connect.Request[pb.TailEventsRequest],
	stream *connect.ServerStream[pb.TailEventsResponse],
) error {
	return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("handler for %s not yet implemented", "TailEvents"))
}
`

// setupVerticalProject lays down the temp project the RPC tests resolve
// against: forge.yaml, go.mod, the tasks handler package (with two
// pb-through unwired stubs), and the descriptor.
func setupVerticalProject(t *testing.T) string {
	t.Helper()
	dir := withTempProject(t, minimalServiceForgeYAML)
	markServiceProject(t, dir)
	writeFixtureFile(t, dir, "go.mod", "module example.com/testproj\n\ngo 1.24\n")
	writeFixtureFile(t, dir, filepath.Join("internal", "handlers", "tasks", "service.go"), fixtureHandlerServiceGo)
	writeFixtureFile(t, dir, filepath.Join("internal", "handlers", "tasks", "handlers.go"), fixtureHandlersGo)

	desc, err := json.MarshalIndent(verticalDescriptor(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, dir, filepath.Join("gen", "forge_descriptor.json"), string(desc))
	return dir
}
