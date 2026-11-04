// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestByKeyMissIsClassifiedNotFound pins the store's half of the svcerr
// contract (research/forge-migration/11-svcerr-plan.md §2.2): every by-key
// read a handler calls answers a missing row with a svcerr NotFound, so a
// handler's bare svcerr.Wrap(err) sends CodeNotFound — never the Internal an
// unclassified error becomes, and never driver text.
//
// A read that has a domain sentinel keeps it (errors.Is still matches it), and
// one that answered sql.ErrNoRows keeps that too, for the callers that treat
// absence as a default.
func TestByKeyMissIsClassifiedNotFound(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	db := env.ORM
	const missing = "does-not-exist"
	user, project := storetest.SeedUserID, storetest.SeedProjectID

	call := func(f func() (any, error)) func() error {
		return func() error { _, err := f(); return err }
	}
	for _, tc := range []struct {
		name     string
		read     func() error
		sentinel error // the domain sentinel the miss must still be, if any
		noRows   bool  // the miss must still be errors.Is(sql.ErrNoRows)
	}{
		// Domain sentinels (internal/db/core).
		{name: "GetChat", read: call(func() (any, error) { return store.GetChat(ctx, db, missing) }), sentinel: core.ErrChatNotFound},
		{name: "GetChatWithUserCheck", read: call(func() (any, error) { return store.GetChatWithUserCheck(ctx, db, missing, user) }), sentinel: core.ErrChatNotFound},
		{name: "GetWorktree", read: call(func() (any, error) { return store.GetWorktree(ctx, db, missing) }), sentinel: core.ErrWorktreeNotFound},
		{name: "GetWorktreeByPath", read: call(func() (any, error) { return store.GetWorktreeByPath(ctx, db, "/nowhere") }), sentinel: core.ErrWorktreeNotFound},
		{name: "GetProjectWithUserCheck", read: call(func() (any, error) { return store.GetProjectWithUserCheck(ctx, db, missing, user) }), sentinel: core.ErrProjectNotFound},
		{name: "GetTrigger", read: call(func() (any, error) { return store.GetTrigger(ctx, db, missing) }), sentinel: core.ErrTriggerNotFound},
		{name: "GetTriggerRegistration", read: call(func() (any, error) { return store.GetTriggerRegistration(ctx, db, missing) }), sentinel: core.ErrTriggerRegistrationNotFound},
		{name: "LoadRunEvent", read: call(func() (any, error) { return store.LoadRunEvent(ctx, db, missing) }), sentinel: core.ErrRunEventNotFound},
		{name: "GetModelEndpoint", read: call(func() (any, error) { return store.GetModelEndpoint(ctx, db, user, missing) }), sentinel: core.ErrModelEndpointNotFound},
		{name: "GetUserProviderAccountSealed", read: call(func() (any, error) { return store.GetUserProviderAccountSealed(ctx, db, user, missing) }), sentinel: core.ErrProviderAccountNotFound},
		{name: "GetConnectionSecrets", read: call(func() (any, error) { return store.GetConnectionSecrets(ctx, db, user, missing) }), sentinel: core.ErrConnectionNotFound},
		{name: "GetPlanByThreadID", read: call(func() (any, error) { return store.GetPlanByThreadID(ctx, db, missing) }), sentinel: core.ErrPlanNotFound, noRows: true},

		// Hand-written reads with no sentinel of their own.
		{name: "GetProject", read: call(func() (any, error) { return store.GetProject(ctx, db, missing) })},
		{name: "GetProjectByPath", read: call(func() (any, error) { return store.GetProjectByPath(ctx, db, "/nowhere") })},
		{name: "GetRepo", read: call(func() (any, error) { return store.GetRepo(ctx, db, missing) })},
		{name: "GetTask", read: call(func() (any, error) { return store.GetTask(ctx, db, missing) })},
		{name: "GetTaskDependency", read: call(func() (any, error) { return store.GetTaskDependency(ctx, db, missing) })},
		{name: "GetPlan", read: call(func() (any, error) { return store.GetPlan(ctx, db, missing) })},
		{name: "LoadWorkflow", read: call(func() (any, error) { return store.LoadWorkflow(ctx, db, missing) })},
		{name: "LoadStepExecution", read: call(func() (any, error) { return store.LoadStepExecution(ctx, db, missing) })},
		{name: "GetSetting", read: call(func() (any, error) { return store.GetSetting(ctx, db, user, nil, "no-such-key") })},
		{name: "GetBackgroundProcess", read: call(func() (any, error) { return store.GetBackgroundProcess(ctx, db, missing) })},

		// Reads that answered a raw sql.ErrNoRows, which callers branch on.
		{name: "GetThread", read: call(func() (any, error) { return store.GetThread(ctx, db, missing) }), noRows: true},
		{name: "GetContextWindow", read: call(func() (any, error) { return store.GetContextWindow(ctx, db, missing) }), noRows: true},
		{name: "GetDaemon", read: call(func() (any, error) { return store.GetDaemon(ctx, db, missing) }), noRows: true},
		{name: "GetPreset", read: call(func() (any, error) { return store.GetPreset(ctx, db, missing) }), noRows: true},
		{name: "GetPresetBySlug", read: call(func() (any, error) { return store.GetPresetBySlug(ctx, db, user, "no-such-slug") }), noRows: true},
		{name: "LoadWorkflowDraft", read: call(func() (any, error) { return store.LoadWorkflowDraft(ctx, db, missing) }), noRows: true},
		{name: "GetItemDefault", read: call(func() (any, error) { return store.GetItemDefault(ctx, db, 1, "no-such-slug") }), noRows: true},
		{name: "GetProjectConfigRecord", read: call(func() (any, error) { return store.GetProjectConfigRecord(ctx, db, project) }), noRows: true},
		{name: "GetAPIKeySealed", read: call(func() (any, error) { return store.GetAPIKeySealed(ctx, db, user, "no-such-provider") }), noRows: true},
		{name: "GetLiveConnectorGrant", read: call(func() (any, error) { return store.GetLiveConnectorGrant(ctx, db, missing) }), noRows: true},
		{name: "GetLiveConnectorClientBinding", read: call(func() (any, error) { return store.GetLiveConnectorClientBinding(ctx, db, user, missing) }), noRows: true},
		{name: "UpdateWorkflowStatus", read: func() error {
			return store.UpdateWorkflowStatus(ctx, db, missing, core.WorkflowStatus{State: core.WorkflowStateActive})
		}, noRows: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.read()
			if !svcerr.IsNotFound(err) {
				t.Fatalf("miss err = %v, want a svcerr NotFound", err)
			}
			wire := svcerr.ToConnect(err)
			if wire.Code() != connect.CodeNotFound {
				t.Errorf("wire code = %v, want %v", wire.Code(), connect.CodeNotFound)
			}
			if svcerr.Classify(err) != svcerr.ClassUser {
				t.Errorf("class = %v, want user: a miss is the caller's, not a server fault", svcerr.Classify(err))
			}
			if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Errorf("miss err = %v, want errors.Is %v", err, tc.sentinel)
			}
			if tc.noRows && !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("miss err = %v, want errors.Is sql.ErrNoRows", err)
			}
		})
	}
}
