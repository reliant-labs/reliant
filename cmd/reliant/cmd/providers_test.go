// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/orgfunding"
)

var updateProvidersGolden = flag.Bool("update-providers-golden", false,
	"rewrite testdata/providers/*.golden from the current output")

// checkProvidersGolden compares got with testdata/providers/<name>.
// Regenerate with: go test ./cmd/reliant/cmd -run 'TestProviders|TestWorkflowFunding' -update-providers-golden
func checkProvidersGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "providers", name)
	if *updateProvidersGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s (regenerate with -update-providers-golden): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("%s differs (regenerate with -update-providers-golden)\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// --- fakes ---------------------------------------------------------------

type fakeProviderAccounts struct {
	reliantv1connect.UnimplementedProviderAccountServiceHandler
	mu       sync.Mutex
	accounts []*reliantv1.ProviderAccount
	question *reliantv1.PersonalFundingQuestion
	reads    int
}

func (f *fakeProviderAccounts) ListProviderAccounts(context.Context, *connect.Request[reliantv1.ListProviderAccountsRequest]) (*connect.Response[reliantv1.ListProviderAccountsResponse], error) {
	return connect.NewResponse(&reliantv1.ListProviderAccountsResponse{Accounts: f.accounts}), nil
}

func (f *fakeProviderAccounts) GetPersonalFundingQuestion(context.Context, *connect.Request[reliantv1.GetPersonalFundingQuestionRequest]) (*connect.Response[reliantv1.GetPersonalFundingQuestionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return connect.NewResponse(&reliantv1.GetPersonalFundingQuestionResponse{Question: f.question}), nil
}

func (f *fakeProviderAccounts) setQuestion(q *reliantv1.PersonalFundingQuestion) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.question = q
}

// fakeOrgProviderAccounts refuses the CLI login as the server does on managed
// Reliant (person_required), unless asPerson.
type fakeOrgProviderAccounts struct {
	reliantv1connect.UnimplementedOrgProviderAccountServiceHandler
	asPerson bool
	keys     []*reliantv1.OrgProviderAccount
}

func (f *fakeOrgProviderAccounts) ListOrgProviderAccounts(context.Context, *connect.Request[reliantv1.ListOrgProviderAccountsRequest]) (*connect.Response[reliantv1.ListOrgProviderAccountsResponse], error) {
	if !f.asPerson {
		return nil, svcerr.ToConnect(svcerr.WithReason(svcerr.PermissionDenied("only a person signed in to the app can list an organization's AI accounts"), "person_required"))
	}
	return connect.NewResponse(&reliantv1.ListOrgProviderAccountsResponse{Accounts: f.keys}), nil
}

type fakeSettings struct {
	reliantv1connect.UnimplementedSettingsServiceHandler
	values map[string]string
}

func (f *fakeSettings) GetSetting(_ context.Context, req *connect.Request[reliantv1.GetSettingRequest]) (*connect.Response[reliantv1.GetSettingResponse], error) {
	v, ok := f.values[req.Msg.GetKey()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	return connect.NewResponse(&reliantv1.GetSettingResponse{Setting: &reliantv1.Setting{Key: req.Msg.GetKey(), Value: v}}), nil
}

type providersHarness struct {
	accounts *fakeProviderAccounts
	org      *fakeOrgProviderAccounts
	settings *fakeSettings
}

func newProvidersHarness(t *testing.T) *providersHarness {
	t.Helper()
	isolateCLI(t)
	h := &providersHarness{
		accounts: &fakeProviderAccounts{},
		org:      &fakeOrgProviderAccounts{},
		settings: &fakeSettings{values: map[string]string{}},
	}
	mux := http.NewServeMux()
	p1, h1 := reliantv1connect.NewProviderAccountServiceHandler(h.accounts)
	mux.Handle(p1, h1)
	p2, h2 := reliantv1connect.NewOrgProviderAccountServiceHandler(h.org)
	mux.Handle(p2, h2)
	p3, h3 := reliantv1connect.NewSettingsServiceHandler(h.settings)
	mux.Handle(p3, h3)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	loginFor(t, srv.URL, "rlat_test000000000000000000000000000")
	t.Setenv(envServerURL, srv.URL)
	return h
}

func (h *providersHarness) run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := NewRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func sampleAccounts() []*reliantv1.ProviderAccount {
	return []*reliantv1.ProviderAccount{
		{Id: "a1", Provider: "claude", Label: "alice@example.com", PlanDisplay: strp("Max 20×"),
			Status: reliantv1.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_AVAILABLE, Funding: reliantv1.ProviderAccountFunding_PROVIDER_ACCOUNT_FUNDING_PERSONAL},
		{Id: "a2", Provider: "anthropic", Label: "anthropic key …a1b2",
			Status: reliantv1.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_PAUSED},
		{Id: "a3", Provider: "claude", Label: "alice@acme.com", PlanDisplay: strp("Team"),
			Status: reliantv1.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_LIMITED, LimitedUntil: strp("2026-11-13T15:40:00Z"),
			Funding: reliantv1.ProviderAccountFunding_PROVIDER_ACCOUNT_FUNDING_ORG_SEAT, FundingOrgId: strp("org-acme")},
		{Id: "a4", Provider: "reliant", Label: "Reliant",
			Status:  reliantv1.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_AVAILABLE,
			Funding: reliantv1.ProviderAccountFunding_PROVIDER_ACCOUNT_FUNDING_ORG_WALLET, FundingOrgId: strp("org-acme")},
	}
}

func strp(s string) *string { return &s }

// --- providers list ------------------------------------------------------

func TestProvidersListGroupsAccountsByWhoPays(t *testing.T) {
	h := newProvidersHarness(t)
	h.accounts.accounts = sampleAccounts()
	stdout, stderr, err := h.run(t, "providers", "list")
	if err != nil {
		t.Fatalf("providers list: %v (stderr %s)", err, stderr)
	}
	checkProvidersGolden(t, "list.golden", stdout)
}

// From the CLI the org's own keys are refused (person_required): the list says
// where to see them, and the person's choices for the org are read and shown.
func TestProvidersListOrgFromTheCLI(t *testing.T) {
	h := newProvidersHarness(t)
	h.accounts.accounts = sampleAccounts()
	h.settings.values[orgfunding.SettingKey] = `{"org-acme":{"order":"org_first","on_org_limit":"wait"}}`
	stdout, stderr, err := h.run(t, "providers", "list", "--org", "org-acme")
	if err != nil {
		t.Fatalf("providers list --org: %v (stderr %s)", err, stderr)
	}
	checkProvidersGolden(t, "list_org_cli.golden", stdout)
}

// Where the server does take the credential as a person (a self-host shared
// key), the org's keys are listed, without their key.
func TestProvidersListOrgAsAPerson(t *testing.T) {
	h := newProvidersHarness(t)
	h.org.asPerson = true
	h.org.keys = []*reliantv1.OrgProviderAccount{{Account: &reliantv1.ProviderAccount{
		Id: "k1", Provider: "anthropic", Label: "Acme prod", Status: reliantv1.ProviderAccountStatus_PROVIDER_ACCOUNT_STATUS_AVAILABLE,
		Funding: reliantv1.ProviderAccountFunding_PROVIDER_ACCOUNT_FUNDING_ORG_KEY, FundingOrgId: strp("org-acme"),
	}}}
	h.settings.values[orgfunding.SettingKey] = `{"org-acme":{"order":"org_only"}}`
	stdout, stderr, err := h.run(t, "providers", "list", "--org", "org-acme")
	if err != nil {
		t.Fatalf("providers list --org: %v (stderr %s)", err, stderr)
	}
	checkProvidersGolden(t, "list_org_person.golden", stdout)
}

func TestProvidersListJSON(t *testing.T) {
	h := newProvidersHarness(t)
	h.accounts.accounts = sampleAccounts()[2:3]
	stdout, stderr, err := h.run(t, "providers", "list", "--org", "org-acme", "--json")
	if err != nil {
		t.Fatalf("providers list --json: %v (stderr %s)", err, stderr)
	}
	checkProvidersGolden(t, "list_org.json.golden", stdout)
}

// --- workflow funding ----------------------------------------------------

func askedQuestion() *reliantv1.PersonalFundingQuestion {
	return &reliantv1.PersonalFundingQuestion{
		ChatId: "chat-1", OrgId: "org-acme", Reason: reliantv1.PersonalFundingQuestion_REASON_LIMITED,
		State: reliantv1.PersonalFundingQuestion_STATE_ASKED, Model: "claude-opus-4",
		PersonalProvider: "claude", PersonalAccountLabel: "alice@example.com",
		OrgResetAt: strp("2026-11-13T15:40:00Z"), AskedAt: "2026-11-13T12:00:00Z",
	}
}

func TestWorkflowFundingShowsAnOpenQuestionAndExits4(t *testing.T) {
	h := newProvidersHarness(t)
	h.accounts.setQuestion(askedQuestion())
	stdout, _, err := h.run(t, "workflow", "funding", "chat-1")
	var open fundingOpenError
	if err == nil || !errors.As(err, &open) || open.ExitCode() != 4 {
		t.Fatalf("want exit 4 for an open question, got %v", err)
	}
	checkProvidersGolden(t, "funding_asked.golden", stdout)
}

func TestWorkflowFundingNoQuestion(t *testing.T) {
	h := newProvidersHarness(t)
	stdout, stderr, err := h.run(t, "workflow", "funding", "chat-1")
	if err != nil {
		t.Fatalf("workflow funding: %v (stderr %s)", err, stderr)
	}
	checkProvidersGolden(t, "funding_none.golden", stdout)
}

func TestWorkflowFundingJSON(t *testing.T) {
	h := newProvidersHarness(t)
	q := askedQuestion()
	q.State = reliantv1.PersonalFundingQuestion_STATE_USE_PERSONAL
	h.accounts.setQuestion(q)
	stdout, stderr, err := h.run(t, "workflow", "funding", "chat-1", "--json")
	if err != nil {
		t.Fatalf("workflow funding --json: %v (stderr %s)", err, stderr)
	}
	checkProvidersGolden(t, "funding_answered.json.golden", stdout)
}

// A follow never prompts and never answers: it prints one line each time the
// run starts waiting on the question (a new asked_at), and the run keeps
// waiting. It reads only, so it cannot spend the person's own account.
func TestFundingWatchPrintsOneLinePerQuestion(t *testing.T) {
	f := &fakeProviderAccounts{}
	f.setQuestion(askedQuestion())
	var out syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watchFundingQuestion(ctx, f, "chat-1", &out, time.Millisecond)
		close(done)
	}()
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.reads >= 5 })
	q := askedQuestion()
	q.AskedAt = "2026-11-13T13:00:00Z" // asked again later
	f.setQuestion(q)
	waitFor(t, func() bool { return strings.Count(out.String(), "\n") >= 2 })
	// Answered: the question is gone, and later reads print nothing more.
	f.mu.Lock()
	f.question = nil
	after := f.reads
	f.mu.Unlock()
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.reads >= after+3 })
	cancel()
	<-done
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want one line per asking (2), got %d:\n%s", len(lines), out.String())
	}
	checkProvidersGolden(t, "funding_notice.golden", lines[0]+"\n")
}

// --- helpers -------------------------------------------------------------

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
