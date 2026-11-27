package codegen

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/reliant-labs/forge/internal/connectmajor"
)

func scanOwnerSrc(t *testing.T, src string, major connectmajor.Major) []CRUDOwnerUse {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return ScanCRUDOwnerSources(fset, f, major)
}

// connectV2OwnerSrc is a connect-go v2 handler file: bare-message handlers,
// no connect import at all (svcerr carries every error).
const connectV2OwnerSrc = `package a

import (
	"context"

	tn "example.com/app/internal/tenancy"
	"github.com/reliant-labs/forge/pkg/orm"

	pb "example.com/app/gen/services/a/v1"
)

func (s *Service) GetA(ctx context.Context, req *pb.GetARequest) (*pb.GetAResponse, error) {
	tid, err := tn.TenantID(ctx)
	if err != nil {
		return nil, err
	}
	_ = orm.WhereEq("tenant_id", tid)
	return nil, nil
}

// load takes another method's request: a helper, not an RPC.
func (s *Service) load(ctx context.Context, req *pb.GetARequest) error {
	owner, err := tn.UserID(ctx)
	if err != nil {
		return err
	}
	_ = orm.WhereEq("tenant_id", owner)
	return nil
}
`

// connect-go v2 CRUD handlers take the bare request message. In a v2
// project the owner scan reads them like v1's, and still skips a helper
// whose message is not the method's own.
func TestScanCRUDOwnerSources_ConnectV2Shapes(t *testing.T) {
	uses := scanOwnerSrc(t, connectV2OwnerSrc, connectmajor.V2)
	if len(uses) != 1 {
		t.Fatalf("want 1 use (GetA), got %d: %+v", len(uses), uses)
	}
	get := uses[0]
	if get.Method != "GetA" || get.Entity != "A" || get.Column != "tenant_id" || !get.Read ||
		get.Source.Kind != CRUDOwnerFromResolver || get.Source.Func != "tn.TenantID" {
		t.Errorf("GetA (v2): resolver not traced: %+v", get)
	}
}

// v1HelperOwnerSrc is a connect-go v1 handler file holding an ordinary
// helper whose name and parameter happen to match the v2 handler shape.
const v1HelperOwnerSrc = `package tasks

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
	owner := claims.UserID
	_ = orm.WhereEq("tenant_id", owner)
	return nil, nil
}

// An ordinary helper on a different type, not a Connect RPC.
func (h *Helper) ListTasks(ctx context.Context, req *pb.ListTasksRequest) error {
	owner, err := tn.TenantID(ctx)
	if err != nil {
		return err
	}
	_ = orm.WhereEq("tenant_id", owner)
	return nil
}
`

// The v2 shape proves nothing without v2 evidence: in a v1 project the
// scan reads exactly what it always did, so a helper shaped like a v2
// handler is no owner-policy evidence.
func TestScanCRUDOwnerSources_V1ProjectIgnoresBareMessageHelper(t *testing.T) {
	for _, major := range []connectmajor.Major{connectmajor.V1, 0} {
		uses := scanOwnerSrc(t, v1HelperOwnerSrc, major)
		if len(uses) != 1 || uses[0].Method != "GetTask" {
			t.Fatalf("major %v: want only GetTask's use, got %+v", major, uses)
		}
	}
	// A v2 project's v1 file (mid-migration: it imports v1's runtime) is
	// read as v1 too.
	if uses := scanOwnerSrc(t, v1HelperOwnerSrc, connectmajor.V2); len(uses) != 1 || uses[0].Method != "GetTask" {
		t.Fatalf("v2 project, v1 file: want only GetTask's use, got %+v", uses)
	}
	// And a v2-shaped file is no evidence in a v1 project unless it says
	// it is v2 code.
	if uses := scanOwnerSrc(t, connectV2OwnerSrc, connectmajor.V1); len(uses) != 0 {
		t.Fatalf("v1 project: bare-message methods must not be handlers, got %+v", uses)
	}
}

// A file importing connectrpc.com/connect/v2 is v2 code whatever the
// project's go.mod was read as.
func TestScanCRUDOwnerSources_FileImportingV2(t *testing.T) {
	src := `package a

import (
	"context"

	cv2 "connectrpc.com/connect/v2"
	tn "example.com/app/internal/tenancy"
	"github.com/reliant-labs/forge/pkg/orm"

	pb "example.com/app/gen/services/a/v1"
)

var _ = cv2.CodeNotFound

func (s *Service) ListAs(ctx context.Context, req *pb.ListAsRequest) (*pb.ListAsResponse, error) {
	tid, err := tn.TenantID(ctx)
	if err != nil {
		return nil, err
	}
	_ = orm.WhereEq("tenant_id", tid)
	return nil, nil
}
`
	uses := scanOwnerSrc(t, src, connectmajor.V1)
	if len(uses) != 1 || uses[0].Method != "ListAs" || uses[0].Entity != "A" {
		t.Fatalf("want ListAs's use, got %+v", uses)
	}
}
