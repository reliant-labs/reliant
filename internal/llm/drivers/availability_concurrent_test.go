// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// barrierReporter answers only once every reporter of its group has been
// asked: asked one after another, the first would wait for the rest forever.
type barrierReporter struct {
	fakeClient
	arrived chan<- struct{}
	release <-chan struct{}
	report  registry.ProviderAvailability
}

func (b *barrierReporter) ReportAvailability(context.Context) (registry.ProviderAvailability, error) {
	b.arrived <- struct{}{}
	<-b.release
	return b.report, nil
}

// Each account's report is a provider catalog round trip when cold. They are
// asked together, so building the drivers costs the slowest report, not their
// sum, and every answer still lands on its own account.
func TestWithAccountAvailability_AsksEveryAccountsReportAtOnce(t *testing.T) {
	t.Parallel()
	synctest.Test(t, testAccountReportsConcurrent)
}

func testAccountReportsConcurrent(t *testing.T) {
	const n = 3 // two Codex accounts and a Copilot one
	arrived := make(chan struct{}, n)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	codexA := &barrierReporter{arrived: arrived, release: release, report: codexServes(codexAPIModel(t, "gpt-5.5"))}
	codexB := &barrierReporter{arrived: arrived, release: release, report: codexServes(codexAPIModel(t, "gpt-5.6-sol"))}
	copilot := &barrierReporter{arrived: arrived, release: release}

	account := func(driver models.DriverID, id string) models.AccountConfig {
		cfg := cfgFor(driver)
		cfg.AccountID = id
		return models.AccountConfig{DriverConfig: cfg}
	}
	accounts := map[models.DriverID][]models.AccountConfig{
		"codex":   {account("codex", "a"), account("codex", "b")},
		"copilot": {account("copilot", "c")},
	}
	byAccount := map[string]*barrierReporter{"a": codexA, "b": codexB, "c": copilot}
	build := func(_ context.Context, id models.DriverID, cfg models.DriverConfig) (registry.Client, error) {
		r := byAccount[cfg.AccountID]
		r.name = string(id)
		return r, nil
	}

	done := make(chan struct{})
	var available models.AvailableDrivers
	go func() {
		available = withAccountAvailability(context.Background(), accounts, build)
		close(done)
	}()
	defer func() {
		unblock()
		<-done
	}()

	// Wait for durable blocking, not elapsed time: a serial implementation
	// parks in its first reporter and cannot produce all n arrivals.
	synctest.Wait()
	require.Len(t, arrived, n, "every account must be asked before any report returns")
	unblock()
	<-done

	require.Len(t, available.Accounts["codex"], 2)
	require.Len(t, available.Accounts["copilot"], 1)
	first, second := available.Accounts["codex"][0], available.Accounts["codex"][1]
	assert.Equal(t, "a", first.AccountID, "accounts keep their order")
	assert.Equal(t, "b", second.AccountID, "accounts keep their order")
	require.NotNil(t, first.Serves)
	require.NotNil(t, second.Serves)
	assert.True(t, first.Serves("gpt-5.5"), "account a's own report serves gpt-5.5")
	assert.False(t, first.Serves("gpt-5.6-sol"), "account a's report is not account b's")
	assert.True(t, second.Serves("gpt-5.6-sol"), "account b's own report serves gpt-5.6-sol")
	assert.False(t, second.Serves("gpt-5.5"), "account b's report is not account a's")
}
