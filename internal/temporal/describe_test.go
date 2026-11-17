// Copyright (c) 2025 Reliant Labs
package temporal

import (
	"context"
	"net"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// describeServer answers DescribeWorkflowExecution with a running execution
// whose one search attribute claims to be a Bool but carries bytes no data
// converter decodes.
type describeServer struct {
	workflowservice.UnimplementedWorkflowServiceServer
}

func (describeServer) GetSystemInfo(context.Context, *workflowservice.GetSystemInfoRequest) (*workflowservice.GetSystemInfoResponse, error) {
	return &workflowservice.GetSystemInfoResponse{}, nil
}

func (describeServer) DescribeWorkflowExecution(_ context.Context, req *workflowservice.DescribeWorkflowExecutionRequest) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Execution: &commonpb.WorkflowExecution{WorkflowId: req.GetExecution().GetWorkflowId(), RunId: "run-1"},
			Status:    enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
			SearchAttributes: &commonpb.SearchAttributes{IndexedFields: map[string]*commonpb.Payload{
				"Broken": {
					Metadata: map[string][]byte{"encoding": []byte("json/plain"), "type": []byte("Bool")},
					Data:     []byte("not json"),
				},
			}},
		},
	}, nil
}

// dialDescribeServer returns a real SDK client connected in-process to
// describeServer.
func dialDescribeServer(t *testing.T) client.Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	workflowservice.RegisterWorkflowServiceServer(srv, describeServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	c, err := client.NewLazyClient(client.Options{
		HostPort: "passthrough:///bufnet",
		ConnectionOptions: client.ConnectionOptions{
			DialOptions: []grpc.DialOption{
				grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			},
		},
	})
	if err != nil {
		t.Fatalf("NewLazyClient: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

// TestDescribeExecutionSurvivesAnUndecodableSearchAttribute pins why
// DescribeExecution keeps the raw describe (reliant#2780): it returns the
// response, while DescribeWorkflow — the sdk's suggested replacement —
// panics on the same execution. If the second half starts failing, the sdk
// no longer panics and the callers can move to DescribeWorkflow.
func TestDescribeExecutionSurvivesAnUndecodableSearchAttribute(t *testing.T) {
	c := dialDescribeServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	desc, err := DescribeExecution(ctx, c, "wf-1", "")
	if err != nil {
		t.Fatalf("DescribeExecution: %v", err)
	}
	if got := desc.GetWorkflowExecutionInfo().GetStatus(); got != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		t.Fatalf("status = %v, want RUNNING", got)
	}
	if got := desc.GetWorkflowExecutionInfo().GetExecution().GetRunId(); got != "run-1" {
		t.Fatalf("run id = %q, want run-1", got)
	}

	panicked := func() (p bool) {
		defer func() { p = recover() != nil }()
		_, _ = c.DescribeWorkflow(ctx, "wf-1", "")
		return false
	}()
	if !panicked {
		t.Fatal("DescribeWorkflow no longer panics on an undecodable search attribute: " +
			"the reason DescribeExecution stays on the raw describe is gone, so migrate its callers (reliant#2780)")
	}
}
