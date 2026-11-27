// Copyright (c) 2025 Reliant Labs
package queuetoken_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/mergequeue/queuetoken"
)

const callerSecret = "queue-caller-secret-at-least-32-bytes!!"

// minter answers a scripted queue mint and counts both kinds.
type minter struct {
	queueErr error
	// noChecks refuses a bot mint that asks for checks.
	noChecks      bool
	queueCalls    int
	botCalls      int
	lastBot       gitcredentialclient.InstallationRequest
	lastAssertion string
	expires       time.Time
}

func (m *minter) MintQueueInstallationToken(_ context.Context, req gitcredentialclient.QueueInstallationRequest) (gitcredentialclient.InstallationToken, error) {
	m.queueCalls++
	m.lastAssertion = req.CallerAssertion
	if m.queueErr != nil {
		return gitcredentialclient.InstallationToken{}, m.queueErr
	}
	return gitcredentialclient.InstallationToken{Token: gitcredentialclient.NewToken("ghs_queue", nil), BotLogin: "reliant-queue[bot]", ExpiresAt: m.expires}, nil
}

func (m *minter) MintInstallationToken(_ context.Context, req gitcredentialclient.InstallationRequest) (gitcredentialclient.InstallationToken, error) {
	m.botCalls++
	m.lastBot = req
	if m.noChecks && req.Permissions["checks"] != "" {
		return gitcredentialclient.InstallationToken{}, gitcredentialclient.ErrAppPermissions
	}
	return gitcredentialclient.InstallationToken{Token: gitcredentialclient.NewToken("ghs_bot", nil), BotLogin: "reliant-labs[bot]", ExpiresAt: m.expires}, nil
}

var repo = &core.PipelineRepo{ID: "r1", Repository: "acme/widgets", CreatedByUserID: "idp|alice"}

func source(t *testing.T, m *minter, secret string, now func() time.Time) *queuetoken.Source {
	t.Helper()
	s, err := queuetoken.New(queuetoken.Options{Minter: m, CallerSecret: secret, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func plain(t *testing.T, tok queuetoken.Token) string {
	t.Helper()
	var s string
	_ = tok.Token.Use(func(v string) error { s = v; return nil })
	return s
}

func TestQueueAppWhenConfigured(t *testing.T) {
	m := &minter{expires: time.Now().Add(time.Hour)}
	tok, err := source(t, m, callerSecret, nil).Token(t.Context(), repo)
	if err != nil || tok.Unenforced || plain(t, tok) != "ghs_queue" || m.botCalls != 0 || m.lastAssertion == "" {
		t.Fatalf("token = %+v (%v), minter %+v", tok, err, m)
	}
}

func TestFallsBackToTheReliantAppUnenforced(t *testing.T) {
	for name, tc := range map[string]struct {
		secret   string
		queueErr error
		queued   int
	}{
		"no caller secret":         {secret: "", queued: 0},
		"short caller secret":      {secret: "short", queued: 0},
		"queue app not configured": {secret: callerSecret, queueErr: gitcredentialclient.ErrQueueAppNotConfigured, queued: 1},
		"queue app not installed":  {secret: callerSecret, queueErr: gitcredentialclient.ErrQueueAppNotInstalled, queued: 1},
	} {
		t.Run(name, func(t *testing.T) {
			m := &minter{queueErr: tc.queueErr, expires: time.Now().Add(time.Hour)}
			tok, err := source(t, m, tc.secret, nil).Token(t.Context(), repo)
			if err != nil || !tok.Unenforced || plain(t, tok) != "ghs_bot" || m.queueCalls != tc.queued {
				t.Fatalf("token = %+v (%v), minter %+v", tok, err, m)
			}
			if m.lastBot.ExternalUserID != "idp|alice" || m.lastBot.Owner != "acme" || m.lastBot.Repo != "widgets" || m.lastBot.Permissions["checks"] != "write" {
				t.Fatalf("bot request = %+v", m.lastBot)
			}
		})
	}
}

func TestOtherQueueErrorsDoNotFallBack(t *testing.T) {
	for _, qerr := range []error{gitcredentialclient.ErrQueueCallerNotWorker, errors.New("control plane down")} {
		m := &minter{queueErr: qerr}
		if _, err := source(t, m, callerSecret, nil).Token(t.Context(), repo); !errors.Is(err, qerr) || m.botCalls != 0 {
			t.Fatalf("%v: err %v, bot calls %d", qerr, err, m.botCalls)
		}
	}
}

func TestCachesUntilNearExpiry(t *testing.T) {
	now := time.Now()
	m := &minter{expires: now.Add(time.Hour)}
	s := source(t, m, callerSecret, func() time.Time { return now })
	for range 3 {
		if _, err := s.Token(t.Context(), repo); err != nil {
			t.Fatal(err)
		}
	}
	if m.queueCalls != 1 {
		t.Fatalf("%d mints, want 1", m.queueCalls)
	}
	now = now.Add(56 * time.Minute)
	if _, err := s.Token(t.Context(), repo); err != nil || m.queueCalls != 2 {
		t.Fatalf("near expiry: %v, %d mints", err, m.queueCalls)
	}
}

func TestClientsLandAsTheTokensIdentity(t *testing.T) {
	m := &minter{queueErr: gitcredentialclient.ErrQueueAppNotConfigured, expires: time.Now().Add(time.Hour)}
	c := &queuetoken.Clients{Source: source(t, m, callerSecret, nil)}
	gh, id, err := c.Client(t.Context(), repo)
	if err != nil || gh == nil || !id.Unenforced || id.Login != "reliant-labs[bot]" {
		t.Fatalf("client = %v %+v %v", gh, id, err)
	}
}

func TestFallbackWithoutChecksPermissionStillLands(t *testing.T) {
	m := &minter{noChecks: true, expires: time.Now().Add(time.Hour)}
	tok, err := source(t, m, "", nil).Token(t.Context(), repo)
	if err != nil || !tok.Unenforced || m.botCalls != 2 || m.lastBot.Permissions != nil {
		t.Fatalf("token = %+v (%v), minter %+v", tok, err, m)
	}
}
