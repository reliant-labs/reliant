// Copyright (c) 2025 Reliant Labs
package forge

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/descriptorpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// readOnlyForgeCommands are the daemon commands that run forge without
// writing: each one's argv is pinned to forge's read-only flag by the
// daemonruntime tests (`--plan` for the promote preview, `--dry-run` for the
// deploy preview), which is what makes dispatching one a read.
var readOnlyForgeCommands = map[string]string{
	"forge.promote_plan": "env deploy <env> <release> --plan",
	"forge.deploy_plan":  "env deploy <env> --dry-run",
}

// TestForgeService_NoSideEffectsRPCsOnlyRead holds every ForgeService rpc
// marked `idempotency_level = NO_SIDE_EFFECTS` to the claim. The marker is not
// documentation: the web generates a cached, refetch-on-focus query for such an
// rpc, and Connect serves it over GET. So each one may read the project row and
// dispatch only a read-only forge command — never write the row, never reach a
// command that builds, pushes or applies.
//
// A newly marked rpc fails here until it has a row in calls, which is where its
// reads are proven.
func TestForgeService_NoSideEffectsRPCsOnlyRead(t *testing.T) {
	calls := map[string]func(*Service) error{
		"PlanPromote": func(s *Service) error {
			_, err := s.PlanPromote(authedCtx(), connect.NewRequest(&reliantv1.PlanForgePromoteRequest{
				ProjectId: "p", Env: "staging", Release: "v1.5.15",
			}))
			return err
		},
		"PlanDeploy": func(s *Service) error {
			_, err := s.PlanDeploy(authedCtx(), connect.NewRequest(&reliantv1.PlanForgeDeployRequest{
				ProjectId: "p", Env: "prod",
			}))
			return err
		},
	}

	methods := reliantv1.File_reliant_v1_forge_proto.Services().ByName("ForgeService").Methods()
	marked := 0
	for i := range methods.Len() {
		method := methods.Get(i)
		options, _ := method.Options().(*descriptorpb.MethodOptions)
		if options.GetIdempotencyLevel() != descriptorpb.MethodOptions_NO_SIDE_EFFECTS {
			continue
		}
		marked++
		name := string(method.Name())
		t.Run(name, func(t *testing.T) {
			call, ok := calls[name]
			if !ok {
				t.Fatalf("%s is marked NO_SIDE_EFFECTS but has no row in calls: add one that proves it only reads", name)
			}

			router := &MockRouter{
				SendDaemonCommandFunc: func(context.Context, string, string, []byte, int32) ([]byte, error) {
					return forgeDaemonReply(t, true, true, 0, `{"dry_run": true}`), nil
				},
			}
			// SetProjectForgeNameFunc is left unset on purpose: the mock
			// answers an unset method with an error, and the recorder below
			// fails the row if anything tried.
			repo := &MockRepository{
				GetProjectWithUserCheckFunc: func(_ context.Context, id, _ string) (*db.Project, error) {
					return &db.Project{ID: id, Path: "/daemon/workspace/control-plane"}, nil
				},
			}

			if err := call(newForgeService(router, repo)); err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			if n := repo.CallCount("SetProjectForgeName"); n != 0 {
				t.Errorf("%s wrote the project row %d time(s); a NO_SIDE_EFFECTS rpc may only read it", name, n)
			}
			dispatched := router.Calls("SendDaemonCommand")
			if len(dispatched) == 0 {
				t.Fatalf("%s dispatched no daemon command; the row no longer exercises its read", name)
			}
			for _, c := range dispatched {
				commandType, _ := c.Args[2].(string)
				if _, ok := readOnlyForgeCommands[commandType]; !ok {
					t.Errorf("%s dispatched %q, which is not a read-only forge command", name, commandType)
				}
			}
		})
	}

	if marked != len(calls) {
		t.Errorf("calls has %d rows but %d ForgeService rpcs are NO_SIDE_EFFECTS: a row outlived its marker", len(calls), marked)
	}
}
