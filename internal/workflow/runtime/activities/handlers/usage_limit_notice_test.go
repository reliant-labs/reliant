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
	a := models.AccountConfig{DriverConfig: models.DriverConfig{DriverID: "claude", AccountID: "a", AccountLabel: "alice@acme.com"}}
	b := models.AccountConfig{DriverConfig: models.DriverConfig{DriverID: "claude", AccountID: "b", AccountLabel: "bob@acme.com"}}
	reset := time.Date(2026, 10, 19, 15, 40, 0, 0, time.UTC)

	assert.Equal(t,
		"Claude account **alice@acme.com** reached its 5-hour limit (resets <time:2026-10-19T15:40:00Z>). Continuing on **bob@acme.com**.",
		accountMoveNotice(accountrouter.Result{Account: b, Previous: &a, Reason: accountrouter.ReasonLimited}, "five_hour", reset))

	assert.Equal(t,
		"Claude account **alice@acme.com** reached its 7-day limit. Continuing on **bob@acme.com**.",
		accountMoveNotice(accountrouter.Result{Account: b, Previous: &a, Reason: accountrouter.ReasonLimited}, "seven_day", time.Time{}))

	for reason, want := range map[string]string{
		accountrouter.ReasonRemoved: "Claude account **alice@acme.com** is no longer available. Continuing on **bob@acme.com**.",
	} {
		assert.Equal(t, want, accountMoveNotice(accountrouter.Result{Account: b, Previous: &a, Reason: reason}, "", time.Time{}), reason)
	}

	// A removed account is no longer in the list: the notice still reads.
	gone := models.AccountConfig{DriverConfig: models.DriverConfig{DriverID: "claude", AccountID: "x"}}
	assert.Equal(t, "Claude account **an account** is no longer available. Continuing on **bob@acme.com**.",
		accountMoveNotice(accountrouter.Result{Account: b, Previous: &gone, Reason: accountrouter.ReasonRemoved}, "", time.Time{}))
}

// The worker does not know the reader's time zone, so a notice carries the
// instant as an RFC 3339 UTC token for the web to render locally, whatever
// zone the worker runs in.
func TestNoticeClockIsAZoneIndependentToken(t *testing.T) {
	orig := time.Local
	t.Cleanup(func() { time.Local = orig })
	at := time.Date(2026, 10, 19, 15, 40, 0, 0, time.UTC)
	for _, zone := range []*time.Location{time.UTC, time.FixedZone("EST", -5*3600), time.FixedZone("JST", 9*3600)} {
		time.Local = zone
		assert.Equal(t, "<time:2026-10-19T15:40:00Z>", noticeClock(at.In(zone)))
	}
}

func TestProviderSwitchNotice_UnknownResetSaysCheckingAgain(t *testing.T) {
	probe := time.Date(2026, 10, 19, 15, 40, 0, 0, time.UTC)
	got := providerSwitchNotice("claude", "gpt-5.6-sol@codex", probe, true)
	assert.Equal(t,
		"Claude is rate-limited on all your accounts; no reset time was given, checking again at <time:2026-10-19T15:40:00Z>. Continuing on **GPT-5.6 Sol · Codex** as this chat allows; returning to Claude once its limit lifts.",
		got)
	assert.NotContains(t, got, "until", "a probe time is not announced as a reset")
}

func TestProviderSwitchAndReturnNotices(t *testing.T) {
	until := time.Date(2026, 10, 19, 15, 40, 0, 0, time.UTC)

	assert.Equal(t,
		"Claude is rate-limited on all your accounts until <time:2026-10-19T15:40:00Z>. Continuing on **GPT-5.6 Sol · Codex** as this chat allows; returning to Claude after the reset.",
		providerSwitchNotice("claude", "gpt-5.6-sol@codex", until, false))

	assert.Equal(t,
		"Claude's limit has reset. Back on **Claude 5.5 Opus · Claude**.",
		providerReturnNotice("claude", "claude-5.5-opus@claude"))
}
