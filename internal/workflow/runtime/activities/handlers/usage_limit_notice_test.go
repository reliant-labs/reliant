// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/llm/accountrouter"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// The words of the three transcript notices are product copy. Pinned so a
// rewording is a decision, not an accident.
func TestAccountMoveNotice(t *testing.T) {
	t.Setenv("TZ", "UTC")
	time.Local = time.UTC
	a := models.AccountConfig{DriverConfig: models.DriverConfig{DriverID: "claude", AccountID: "a", AccountLabel: "alice@acme.com"}}
	b := models.AccountConfig{DriverConfig: models.DriverConfig{DriverID: "claude", AccountID: "b", AccountLabel: "bob@acme.com"}}
	reset := time.Date(2026, 10, 19, 15, 40, 0, 0, time.UTC)

	assert.Equal(t,
		"Claude account **alice@acme.com** reached its 5-hour limit (resets 3:40 PM). Continuing on **bob@acme.com**.",
		accountMoveNotice(accountrouter.Result{Account: b, Previous: &a, Reason: accountrouter.ReasonLimited}, "five_hour", reset))

	assert.Equal(t,
		"Claude account **alice@acme.com** reached its 7-day limit. Continuing on **bob@acme.com**.",
		accountMoveNotice(accountrouter.Result{Account: b, Previous: &a, Reason: accountrouter.ReasonLimited}, "seven_day", time.Time{}))

	for reason, want := range map[string]string{
		accountrouter.ReasonPaused:      "Claude account **alice@acme.com** was paused. Continuing on **bob@acme.com**.",
		accountrouter.ReasonNeedsReauth: "Claude account **alice@acme.com** needs sign-in. Continuing on **bob@acme.com**.",
		accountrouter.ReasonRemoved:     "Claude account **alice@acme.com** was removed. Continuing on **bob@acme.com**.",
	} {
		assert.Equal(t, want, accountMoveNotice(accountrouter.Result{Account: b, Previous: &a, Reason: reason}, "", time.Time{}), reason)
	}

	// A removed account is no longer in the list: the notice still reads.
	gone := models.AccountConfig{DriverConfig: models.DriverConfig{DriverID: "claude", AccountID: "x"}}
	assert.Equal(t, "Claude account **an account** was removed. Continuing on **bob@acme.com**.",
		accountMoveNotice(accountrouter.Result{Account: b, Previous: &gone, Reason: accountrouter.ReasonRemoved}, "", time.Time{}))
}

func TestProviderSwitchAndReturnNotices(t *testing.T) {
	time.Local = time.UTC
	until := time.Date(2026, 10, 19, 15, 40, 0, 0, time.UTC)

	assert.Equal(t,
		"Claude is rate-limited on all your accounts until 3:40 PM. Continuing on **GPT-5.6 Sol · Codex** as this chat allows; returning to Claude after the reset.",
		providerSwitchNotice("claude", "gpt-5.6-sol@codex", until))

	assert.Equal(t,
		"Claude's limit has reset. Back on **Claude 5.5 Opus · Claude**.",
		providerReturnNotice("claude", "claude-5.5-opus@claude"))
}
