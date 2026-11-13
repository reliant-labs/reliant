// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// adoptClock stamps the fixtures: no assertion reads the time.
var adoptClock = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

// adoptMembers is membership where everyone belongs to acme and globex except
// the users in left.
type adoptMembers struct{ left map[string]bool }

func (m adoptMembers) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return (orgID == "acme" || orgID == "globex") && !m.left[userID], true, nil
}

// adoptEnv is a creator who made automations in acme and then left it, and
// bob, a live member of acme with a project, a machine and a connection of
// his own, who adopts them.
type adoptEnv struct {
	*triggerTestEnv
	creatorCtx   context.Context // the creator, still a member: how they made them
	bobID        string
	bobCtx       context.Context // bob, with the creator gone from acme
	bobProject   string
	bobDaemon    string
	creatorWT    string
	acmeProject  string
	globexBobPrj string
}

func setupAdoption(t *testing.T) *adoptEnv {
	t.Helper()
	env, _ := setupInboundTriggerTest(t)
	acme, globex := "acme", "globex"
	e := &adoptEnv{triggerTestEnv: env, bobID: uuid.NewString()}
	e.creatorCtx = authz.WithMembership(env.ctx, adoptMembers{})
	e.bobCtx = authz.WithMembership(auth.WithCredential(
		context.WithValue(context.Background(), auth.UserIDContextKey, e.bobID), auth.CredentialSession),
		adoptMembers{left: map[string]bool{env.userID: true}})
	e.acmeProject = env.orgProject(t, &acme)
	e.bobProject = e.project(t, e.bobID, &acme)
	e.globexBobPrj = e.project(t, e.bobID, &globex)
	e.bobDaemon = uuid.NewString()
	require.NoError(t, env.repo.UpsertDaemon(context.Background(), &db.Daemon{ID: e.bobDaemon, UserID: e.bobID}))
	e.creatorWT = uuid.NewString()
	require.NoError(t, env.repo.CreateWorktree(context.Background(), &db.Worktree{
		ID: e.creatorWT, Name: "feature", Path: t.TempDir(), Branch: "feature", BaseBranch: "main",
		ProjectID: e.acmeProject, Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
	}))
	return e
}

func (e *adoptEnv) project(t *testing.T, userID string, org *string) string {
	t.Helper()
	id := uuid.NewString()
	now := adoptClock
	require.NoError(t, e.repo.CreateProject(context.Background(), &db.Project{
		ID: id, UserID: userID, OrgID: org, Name: "p-" + id[:6], Path: t.TempDir(), IsGitRepo: true,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return id
}

// made is a trigger the creator made in acme, read back as stored.
func (e *adoptEnv) made(t *testing.T, def *reliantv1.TriggerDefinition) (*core.Trigger, *reliantv1.CreateTriggerResponse) {
	t.Helper()
	def.ProjectId = e.acmeProject
	resp, err := e.svc.CreateTrigger(e.creatorCtx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: def}))
	require.NoError(t, err)
	return e.row(t, resp.Msg.GetTrigger().GetId()), resp.Msg
}

func (e *adoptEnv) row(t *testing.T, id string) *core.Trigger {
	t.Helper()
	row, err := e.repo.GetTrigger(context.Background(), id)
	require.NoError(t, err)
	return row
}

// rebinding is bob's: his project and his machine.
func (e *adoptEnv) rebinding(mutate func(*reliantv1.TriggerRebinding)) *reliantv1.TriggerRebinding {
	rb := &reliantv1.TriggerRebinding{ProjectId: e.bobProject, DaemonId: e.bobDaemon}
	if mutate != nil {
		mutate(rb)
	}
	return rb
}

// Every credential-bearing reference the departed owner had (org-sharing.md
// §8.4) is cleared or re-supplied by the adopter, never copied: one case per
// row of the table. Each case also checks that the old owner's reference,
// named in the rebinding, is refused (a personal credential never
// transfers), and that nothing changed when it was.
func TestAdoptAs_EveryCredentialBearingReferenceIsClearedOrReResolved(t *testing.T) {
	t.Parallel()

	cases := []struct {
		row string
		// make creates the creator's trigger; it may return what the
		// checks need (the old webhook token).
		make func(t *testing.T, e *adoptEnv) (*core.Trigger, string)
		// refused, when set, is a rebinding naming what was the old owner's,
		// with the code it is refused with.
		refused     func(e *adoptEnv, old *core.Trigger) *reliantv1.TriggerRebinding
		refusedCode connect.Code
		rebind      func(t *testing.T, e *adoptEnv, rb *reliantv1.TriggerRebinding)
		check       func(t *testing.T, e *adoptEnv, old, adopted *core.Trigger, resp *reliantv1.AcceptTriggerOfferResponse, oldToken string)
	}{
		{
			row: "connection_id naming their personal connection: cleared, the adopter's own bound",
			make: func(t *testing.T, e *adoptEnv) (*core.Trigger, string) {
				theirs := e.createConnection(t, e.userID, "test", "creator", core.ConnectionStatusActive)
				row, _ := e.made(t, e.definition(func(d *reliantv1.TriggerDefinition) {
					d.Name = "on event"
					d.ConnectionId = &theirs.ID
					d.Source = &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{Integration: "test", Events: []string{"x.happened"}}}
				}))
				return row, ""
			},
			refused: func(e *adoptEnv, old *core.Trigger) *reliantv1.TriggerRebinding {
				return e.rebinding(func(rb *reliantv1.TriggerRebinding) { rb.ConnectionId = old.ConnectionID })
			},
			refusedCode: connect.CodeNotFound,
			rebind: func(t *testing.T, e *adoptEnv, rb *reliantv1.TriggerRebinding) {
				mine := e.createConnection(t, e.bobID, "test", "bob", core.ConnectionStatusActive)
				rb.ConnectionId = &mine.ID
			},
			check: func(t *testing.T, e *adoptEnv, old, adopted *core.Trigger, _ *reliantv1.AcceptTriggerOfferResponse, _ string) {
				require.NotNil(t, adopted.ConnectionID)
				assert.NotEqual(t, *old.ConnectionID, *adopted.ConnectionID, "the departed owner's connection is never kept")
				conn, err := e.repo.Connections().GetConnection(context.Background(), e.bobID, *adopted.ConnectionID)
				require.NoError(t, err)
				assert.Equal(t, e.bobID, conn.UserID, "it listens through the adopter's own connection")
			},
		},
		{
			row: "daemon_id and worktree_id: cleared, the adopter's machine",
			make: func(t *testing.T, e *adoptEnv) (*core.Trigger, string) {
				row, _ := e.made(t, e.definition(func(d *reliantv1.TriggerDefinition) { d.WorktreeId = &e.creatorWT }))
				return row, ""
			},
			refused: func(e *adoptEnv, old *core.Trigger) *reliantv1.TriggerRebinding {
				return e.rebinding(func(rb *reliantv1.TriggerRebinding) { rb.DaemonId = old.DaemonID })
			},
			refusedCode: connect.CodeNotFound,
			check: func(t *testing.T, e *adoptEnv, old, adopted *core.Trigger, _ *reliantv1.AcceptTriggerOfferResponse, _ string) {
				assert.Equal(t, e.daemonID, old.DaemonID)
				assert.Equal(t, e.bobDaemon, adopted.DaemonID, "the adopter's machine")
				assert.Nil(t, adopted.WorktreeID, "the old owner's worktree is not carried")
			},
		},
		{
			row: "project_id: the adopter's own project, in the trigger's org",
			make: func(t *testing.T, e *adoptEnv) (*core.Trigger, string) {
				row, _ := e.made(t, e.definition(nil))
				return row, ""
			},
			refused: func(e *adoptEnv, old *core.Trigger) *reliantv1.TriggerRebinding {
				return e.rebinding(func(rb *reliantv1.TriggerRebinding) { rb.ProjectId = old.ProjectID })
			},
			refusedCode: connect.CodeNotFound,
			check: func(t *testing.T, e *adoptEnv, old, adopted *core.Trigger, _ *reliantv1.AcceptTriggerOfferResponse, _ string) {
				assert.Equal(t, e.acmeProject, old.ProjectID)
				assert.Equal(t, e.bobProject, adopted.ProjectID)
				// A project of the adopter's in another org is refused: the
				// trigger's org is fixed.
				_, err := e.svc.AdoptAs(e.bobCtx, e.row(t, adopted.ID), e.bobID,
					e.rebinding(func(rb *reliantv1.TriggerRebinding) { rb.ProjectId = e.globexBobPrj }), "")
				assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "error: %v", err)
			},
		},
		{
			row: "webhook secret: rotated, never carried",
			make: func(t *testing.T, e *adoptEnv) (*core.Trigger, string) {
				secret := "creators-secret"
				row, resp := e.made(t, webhookDefinition(e.triggerTestEnv, func(d *reliantv1.TriggerDefinition) {
					d.Source = &reliantv1.TriggerDefinition_Webhook{Webhook: &reliantv1.WebhookSource{
						Hmac: &reliantv1.WebhookHmac{Header: "X-Hub-Signature-256", Prefix: "sha256="},
					}}
					d.WebhookHmacSecret = &secret
				}))
				return row, resp.GetWebhook().GetToken()
			},
			// Without a new secret: the old one is not carried, so it is
			// required.
			refused: func(e *adoptEnv, _ *core.Trigger) *reliantv1.TriggerRebinding {
				return e.rebinding(nil)
			},
			refusedCode: connect.CodeInvalidArgument,
			rebind: func(_ *testing.T, _ *adoptEnv, rb *reliantv1.TriggerRebinding) {
				secret := "bobs-secret"
				rb.WebhookHmacSecret = &secret
			},
			check: func(t *testing.T, e *adoptEnv, old, adopted *core.Trigger, resp *reliantv1.AcceptTriggerOfferResponse, oldToken string) {
				creds, err := e.repo.GetTriggerWebhookCredentials(context.Background(), adopted.ID)
				require.NoError(t, err)
				fresh := resp.GetWebhook().GetToken()
				require.NotEmpty(t, fresh, "the adopter is shown a new token once")
				assert.NotEqual(t, oldToken, fresh)
				oldHash, newHash := sha256.Sum256([]byte(oldToken)), sha256.Sum256([]byte(fresh))
				assert.NotEqual(t, oldHash[:], creds.TokenHash, "the old token no longer opens the trigger")
				assert.Equal(t, newHash[:], creds.TokenHash)
				assert.Equal(t, "sealed:"+e.bobID+":"+string(triggers.WebhookSecretAAD(adopted.ID))+":bobs-secret", string(creds.SecretSealed),
					"the secret is the adopter's, sealed under the adopter's tenant")
				assert.NotContains(t, string(creds.SecretSealed), "creators-secret")
				assert.Equal(t, old.ID, adopted.ID, "its id, and so its webhook URL, survive")
			},
		},
		{
			row: "presets: re-resolved as the adopter's; one they cannot resolve refuses",
			make: func(t *testing.T, e *adoptEnv) (*core.Trigger, string) {
				_, err := e.repo.CreatePreset(context.Background(), &db.Preset{
					ID: uuid.NewString(), UserID: e.userID, Name: "Creators Review", Slug: "creators-review", Tag: "agent",
					Params: map[string]any{}, CreatedAt: adoptClock, UpdatedAt: adoptClock,
				})
				require.NoError(t, err)
				row, _ := e.made(t, e.definition(func(d *reliantv1.TriggerDefinition) {
					d.Presets = map[string]string{"": "creators-review"}
				}))
				return row, ""
			},
			// The trigger's preset names, kept: the creator's preset does not
			// exist for the adopter.
			refused: func(e *adoptEnv, _ *core.Trigger) *reliantv1.TriggerRebinding {
				return e.rebinding(nil)
			},
			refusedCode: connect.CodeFailedPrecondition,
			rebind: func(_ *testing.T, _ *adoptEnv, rb *reliantv1.TriggerRebinding) {
				rb.Presets = map[string]string{"": "general"}
			},
			check: func(t *testing.T, _ *adoptEnv, _, adopted *core.Trigger, _ *reliantv1.AcceptTriggerOfferResponse, _ string) {
				assert.Equal(t, map[string]string{"": "general"}, adopted.Presets)
			},
		},
		{
			row: "params: the departed owner's values are cleared; the adopter supplies their own",
			make: func(t *testing.T, e *adoptEnv) (*core.Trigger, string) {
				row, _ := e.made(t, e.definition(func(d *reliantv1.TriggerDefinition) {
					d.Params = map[string]*structpb.Value{"api_token": structpb.NewStringValue("creators-token-hunter2"), "channel": structpb.NewStringValue("#ops")}
				}))
				return row, ""
			},
			rebind: func(_ *testing.T, _ *adoptEnv, rb *reliantv1.TriggerRebinding) {
				rb.Params = map[string]*structpb.Value{"channel": structpb.NewStringValue("#bob")}
			},
			check: func(t *testing.T, e *adoptEnv, old, adopted *core.Trigger, resp *reliantv1.AcceptTriggerOfferResponse, _ string) {
				assert.Equal(t, "creators-token-hunter2", old.Params["api_token"])
				// The row is what every run of it launches with: the fire path
				// re-reads it and applies its params as the run's inputs.
				assert.Equal(t, map[string]any{"channel": "#bob"}, adopted.Params, "only the adopter's values")
				assert.NotContains(t, resp.GetTrigger().String(), "hunter2", "the response carries none of the old owner's values")
				got, err := e.svc.GetTrigger(e.bobCtx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: adopted.ID}))
				require.NoError(t, err)
				assert.NotContains(t, got.Msg.GetTrigger().String(), "hunter2", "nor does the adopter's own read of it")
				var stored string
				require.NoError(t, e.repo.ORM().Bun().NewRaw(`SELECT params::text FROM triggers WHERE id = ?`, adopted.ID).Scan(context.Background(), &stored))
				assert.NotContains(t, stored, "hunter2", "the value is gone from the row itself")
			},
		},
		{
			row: "provider account and LLM key: not carried; runs mint for the adopter",
			make: func(t *testing.T, e *adoptEnv) (*core.Trigger, string) {
				row, _ := e.made(t, e.definition(nil))
				return row, ""
			},
			check: func(t *testing.T, e *adoptEnv, old, adopted *core.Trigger, _ *reliantv1.AcceptTriggerOfferResponse, _ string) {
				assert.Equal(t, e.userID, old.UserID)
				assert.Equal(t, e.bobID, adopted.UserID, "the owner, whose provider accounts every run uses, is the adopter")
			},
		},
		{
			row: "authority (scopes): not carried; runs are clipped to the adopter's grants",
			make: func(t *testing.T, e *adoptEnv) (*core.Trigger, string) {
				row, _ := e.made(t, e.definition(nil))
				return row, ""
			},
			check: func(t *testing.T, e *adoptEnv, _, adopted *core.Trigger, _ *reliantv1.AcceptTriggerOfferResponse, _ string) {
				// A run mints for the trigger's owner (MintForUser); nothing
				// on the row carries the old owner's grants.
				assert.Equal(t, e.bobID, adopted.UserID)
				assert.False(t, adopted.Enabled, "and no automation grant is minted until the adopter enables it")
			},
		},
		{
			row: "spend: the trigger's org pays, under the adopter's membership",
			make: func(t *testing.T, e *adoptEnv) (*core.Trigger, string) {
				row, _ := e.made(t, e.definition(nil))
				return row, ""
			},
			check: func(t *testing.T, _ *adoptEnv, old, adopted *core.Trigger, _ *reliantv1.AcceptTriggerOfferResponse, _ string) {
				require.NotNil(t, adopted.OrgID)
				assert.Equal(t, *old.OrgID, *adopted.OrgID, "the org it bills is fixed")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.row, func(t *testing.T) {
			t.Parallel()
			e := setupAdoption(t)
			old, oldToken := tc.make(t, e)

			if tc.refused != nil {
				_, err := e.svc.AdoptAs(e.bobCtx, old, e.bobID, tc.refused(e, old), "")
				assert.Equal(t, tc.refusedCode, svcerr.Code(err), "error: %v", err)
				unchanged := e.row(t, old.ID)
				assert.Equal(t, e.userID, unchanged.UserID, "a refused adoption changes nothing")
				if old.Kind == core.TriggerKindWebhook {
					creds, err := e.repo.GetTriggerWebhookCredentials(context.Background(), old.ID)
					require.NoError(t, err)
					h := sha256.Sum256([]byte(oldToken))
					assert.Equal(t, h[:], creds.TokenHash, "a refused adoption leaves the token")
				}
			}

			rb := e.rebinding(nil)
			if tc.rebind != nil {
				tc.rebind(t, e, rb)
			}
			resp, err := e.svc.AdoptAs(e.bobCtx, old, e.bobID, rb, "")
			require.NoError(t, err)
			adopted := e.row(t, old.ID)
			assert.False(t, adopted.Enabled, "an adopted trigger starts disabled")
			tc.check(t, e, old, adopted, resp, oldToken)
		})
	}
}

// An adopted trigger starts disabled, with no system pause reason, and its
// history records the adoption. Turning it on runs the existing gates as the
// adopter: a GitHub-style trigger refreshes the ADOPTER's access, and a
// refusal there keeps it off. The departed creator can no longer reach it.
func TestAdoptAs_AdoptedTriggerIsDisabledAndEnablingGatesAsTheAdopter(t *testing.T) {
	t.Parallel()
	e := setupAdoption(t)
	access := &fakeAccess{}
	e.configure(t, func(d *Deps) {
		d.Catalog = fakeCatalog{"test": true, "gh": true}
		d.Access = map[string]IntegrationAccess{"gh": access}
	})
	old, _ := e.made(t, ghDefinition(e.triggerTestEnv, nil))
	paused, err := e.repo.PauseTrigger(context.Background(), old.ID, core.TriggerPausedCreatorLeftOrg)
	require.NoError(t, err)
	require.True(t, paused)
	old = e.row(t, old.ID)

	resp, err := e.svc.AdoptAs(e.bobCtx, old, e.bobID, e.rebinding(nil), "")
	require.NoError(t, err)
	assert.False(t, resp.GetTrigger().GetEnabled())
	assert.Empty(t, resp.GetTrigger().GetPausedReason(), "no longer paused for its creator leaving")
	assert.NotEmpty(t, resp.GetNotices())
	assert.NotContains(t, access.users(), e.bobID, "adoption itself enables nothing, so checks nothing yet")

	got, err := e.svc.GetTrigger(e.bobCtx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: old.ID}))
	require.NoError(t, err)
	var ops []string
	for _, c := range got.Msg.GetTrigger().GetChanges() {
		ops = append(ops, c.GetOperation())
	}
	assert.Contains(t, ops, "adopted", "one trigger_changes entry records the adoption")

	enable := func(ctx context.Context) error {
		_, err := e.svc.SetTriggerEnabled(ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: old.ID, Enabled: true}))
		return err
	}
	// The departed creator is not its owner any more.
	departed := authz.WithMembership(e.ctx, adoptMembers{left: map[string]bool{e.userID: true}})
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(enable(departed)))

	access.err, access.permanent = assert.AnError, true
	err = enable(e.bobCtx)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "error: %v", err)
	users := access.users()
	require.NotEmpty(t, users)
	assert.Equal(t, e.bobID, users[len(users)-1], "the gate checked the ADOPTER's access")
	assert.False(t, e.row(t, old.ID).Enabled, "a refused gate keeps it off")

	access.err, access.permanent = nil, false
	require.NoError(t, enable(e.bobCtx))
	assert.True(t, e.row(t, old.ID).Enabled)
}

// An adoption decided on one owner is refused once the trigger changed hands
// (someone adopted it first, its creator rejoined): nothing is written.
func TestAdoptTrigger_RefusedOnceTheOwnerChanged(t *testing.T) {
	t.Parallel()
	e := setupAdoption(t)
	old, _ := e.made(t, e.definition(nil))
	stale := *old
	stale.UserID = e.bobID
	stale.ProjectID = e.bobProject
	stale.DaemonID = e.bobDaemon
	stale.Enabled = false
	err := e.repo.AdoptTrigger(context.Background(), &stale, "someone-else", "")
	assert.ErrorIs(t, err, core.ErrTriggerOwnerChanged)
	assert.Equal(t, e.userID, e.row(t, old.ID).UserID)
}

// RenderDefinition shows an orphan to someone who does not own it: the
// definition only. Param values, the connection and the last firing's event
// payload are withheld; the param names say what is set.
func TestRenderDefinition_WithholdsValues(t *testing.T) {
	t.Parallel()
	e := setupAdoption(t)
	theirs := e.createConnection(t, e.userID, "test", "creator", core.ConnectionStatusActive)
	old, _ := e.made(t, e.definition(func(d *reliantv1.TriggerDefinition) {
		d.Name = "with params"
		d.ConnectionId = &theirs.ID
		d.Params = map[string]*structpb.Value{"api_token": structpb.NewStringValue("hunter2"), "channel": structpb.NewStringValue("#ops")}
		d.Source = &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{Integration: "test", Events: []string{"x.happened"}}}
	}))

	rendered, names := e.svc.RenderDefinition(e.bobCtx, old)
	assert.Equal(t, []string{"api_token", "channel"}, names)
	assert.Empty(t, rendered.GetParams(), "param values are withheld")
	assert.Nil(t, rendered.ConnectionId, "the departed owner's connection is withheld")
	assert.NotContains(t, rendered.String(), "hunter2")
	assert.Equal(t, old.Name, rendered.GetName(), "the definition is shown")
}
