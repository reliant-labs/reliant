// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/projectaccess"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/pkg/config"
)

// The handler's firings and webhook-secret stores are required roles. The
// repository facade no longer carries those two methods, so it cannot stand in
// for them, and a Deps without either is refused at construction, rather than
// a list view or a secret write finding out at run time.
func TestDepsRefuseAMissingFiringsOrWebhookSecretStore(t *testing.T) {
	var facade any = (*db.Repo)(nil)
	_, firings := facade.(Firings)
	_, secrets := facade.(WebhookSecrets)
	require.False(t, firings || secrets, "*db.Repo must not satisfy Firings or WebhookSecrets")
	_, rows := facade.(Triggers)
	_, reads := facade.(FiringReads)
	_, regs := facade.(RegistrationReads)
	_, writes := facade.(TriggerWrites)
	require.False(t, rows || reads || regs || writes, "*db.Repo must not satisfy the trigger row, firing, registration or write roles")

	// Every other required dependency present: only the role under test is missing.
	full := Deps{
		Logger: slog.Default(), Config: &config.Config{}, Repo: &MockRepository{}, Chats: &MockChats{}, Owners: &MockOwners{},
		Firings: store.NewTriggerEventExtStore(nil), WebhookSecrets: store.NewTriggerExtStore(nil), TriggerWrites: store.NewTriggerWritesExtStore(nil), Triggers: store.NewTriggerExtStore(nil), FiringReads: store.NewTriggerEventExtStore(nil), Registrations: store.NewTriggerRegistrationExtStore(nil),
		Projects: &projectaccess.MockService{}, OrgMoves: &MockOrgMoveResumer{}, Pools: &MockPools{},
	}
	require.NotContains(t, errString(full.validateDeps()), "Firings", "the full Deps passes the roles under test")

	noFirings := full
	noFirings.Firings = nil
	require.ErrorContains(t, noFirings.validateDeps(), "Deps.Firings, WebhookSecrets, TriggerWrites, Triggers, FiringReads and Registrations are required")
	noSecrets := full
	noSecrets.WebhookSecrets = nil
	require.ErrorContains(t, noSecrets.validateDeps(), "Deps.Firings, WebhookSecrets, TriggerWrites, Triggers, FiringReads and Registrations are required")
	noRows := full
	noRows.Triggers = nil
	require.ErrorContains(t, noRows.validateDeps(), "Triggers, FiringReads and Registrations are required")
	noFiringReads := full
	noFiringReads.FiringReads = nil
	require.ErrorContains(t, noFiringReads.validateDeps(), "FiringReads")
	noRegistrations := full
	noRegistrations.Registrations = nil
	require.ErrorContains(t, noRegistrations.validateDeps(), "Registrations")
	noWrites := full
	noWrites.TriggerWrites = nil
	require.ErrorContains(t, noWrites.validateDeps(), "Deps.Firings, WebhookSecrets, TriggerWrites, Triggers, FiringReads and Registrations are required")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// realStores gives deps over a real repository the generated stores the
// handler requires beside it (Firings, WebhookSecrets and TriggerWrites), in place of the
// loud mocks the harness would default them to.
func realStores(repo *db.Repo, d Deps) Deps {
	if d.Firings == nil {
		d.Firings = store.NewTriggerEventExtStore(repo.ORM())
	}
	if d.WebhookSecrets == nil {
		d.WebhookSecrets = store.NewTriggerExtStore(repo.ORM())
	}
	if d.TriggerWrites == nil {
		d.TriggerWrites = store.NewTriggerWritesExtStore(repo.ORM())
	}
	if d.Triggers == nil {
		d.Triggers = store.NewTriggerExtStore(repo.ORM())
	}
	if d.FiringReads == nil {
		d.FiringReads = store.NewTriggerEventExtStore(repo.ORM())
	}
	if d.Registrations == nil {
		d.Registrations = store.NewTriggerRegistrationExtStore(repo.ORM())
	}
	return d
}
