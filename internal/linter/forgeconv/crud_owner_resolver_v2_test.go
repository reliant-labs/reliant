package forgeconv

import (
	"testing"
)

const (
	v1ProjectGoMod = "module example.com/app\n\ngo 1.26\n\nrequire (\n\tconnectrpc.com/connect v1.21.1\n\tconnectrpc.com/connect/v2 v2.0.2 // indirect\n)\n"
	v2ProjectGoMod = "module example.com/app\n\ngo 1.26\n\nrequire connectrpc.com/connect/v2 v2.0.2\n"
)

// crudOwnerV1HelperFile is a connect-go v1 handler file whose GetTask keeps
// forge's placeholder beside an ordinary helper — on another type, not an
// RPC — that resolves the tenant. The helper's name and parameter match the
// v2 handler shape, which proves nothing in v1 code.
const crudOwnerV1HelperFile = `package tasks

import (
	"context"

	"connectrpc.com/connect"
	pb "example.com/app/gen/tasks/v1"
	"github.com/reliant-labs/forge/pkg/middleware"
	"github.com/reliant-labs/forge/pkg/orm"
	tn "example.com/app/internal/tenancy"
)

func (s *Service) GetTask(ctx context.Context, req *connect.Request[pb.GetTaskRequest]) (*connect.Response[pb.GetTaskResponse], error) {
	claims, err := middleware.GetUser(ctx)
	if err != nil {
		return nil, err
	}
	// FORGE_SCAFFOLD: placeholder
	owner := claims.UserID
	_ = orm.WhereEq("tenant_id", owner)
	return nil, nil
}

// This is an ordinary helper on a different type, not a Connect RPC.
func (h *Helper) ListTasks(ctx context.Context, req *pb.ListTasksRequest) error {
	owner, err := tn.TenantID(ctx)
	if err != nil {
		return err
	}
	_ = orm.WhereEq("tenant_id", owner)
	return nil
}
`

// crudOwnerV2File is the same disagreement in connect-go v2 handlers, which
// take the bare request message and import no connect package.
const crudOwnerV2File = `package tasks

import (
	"context"

	pb "example.com/app/gen/tasks/v1"
	"github.com/reliant-labs/forge/pkg/middleware"
	"github.com/reliant-labs/forge/pkg/orm"
	tn "example.com/app/internal/tenancy"
)

func (s *Service) GetTask(ctx context.Context, req *pb.GetTaskRequest) (*pb.GetTaskResponse, error) {
	claims, err := middleware.GetUser(ctx)
	if err != nil {
		return nil, err
	}
	// FORGE_SCAFFOLD: placeholder
	owner := claims.UserID
	_ = orm.WhereEq("tenant_id", owner)
	return nil, nil
}

func (s *Service) ListTasks(ctx context.Context, req *pb.ListTasksRequest) (*pb.ListTasksResponse, error) {
	owner, err := tn.TenantID(ctx)
	if err != nil {
		return nil, err
	}
	_ = orm.WhereEq("tenant_id", owner)
	return nil, nil
}
`

// forgeconv-crud-owner-resolver reads connect-go v2's bare-message handlers
// only on v2 evidence. In a v1 project an ordinary helper shaped like a v2
// handler is not a sibling wrapper, so the rule says what it said before v2
// support existed: nothing. In a v2 project the same disagreement between
// real handlers is the finding.
func TestCRUDOwnerResolver_ConnectMajorGatesBareMessageHandlers(t *testing.T) {
	for _, tt := range []struct {
		name, gomod, file string
		want              int
	}{
		{"v1 project, v1 helper", v1ProjectGoMod, crudOwnerV1HelperFile, 0},
		{"no go.mod, v1 helper", "", crudOwnerV1HelperFile, 0},
		// A v2 project mid-migration still holds v1 files; one that imports
		// v1's runtime reads as v1.
		{"v2 project, v1 file", v2ProjectGoMod, crudOwnerV1HelperFile, 0},
		{"v1 project, v2-shaped file", v1ProjectGoMod, crudOwnerV2File, 0},
		{"v2 project, v2 handlers", v2ProjectGoMod, crudOwnerV2File, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"internal/handlers/tasks/handlers_crud.go": tt.file}
			if tt.gomod != "" {
				files["go.mod"] = tt.gomod
			}
			res, err := LintHandlerErrorMapping(writeHandlerFixture(t, files))
			if err != nil {
				t.Fatal(err)
			}
			got := findingsForRule(res.Findings, crudOwnerResolverRule)
			if len(got) != tt.want {
				t.Fatalf("expected %d %s findings, got %d:\n%s", tt.want, crudOwnerResolverRule, len(got), res.FormatText())
			}
			if tt.want == 1 && (got[0].Severity != SeverityError || got[0].Line != 12) {
				t.Errorf("want an error on GetTask (line 12), got %+v", got[0])
			}
		})
	}
}
