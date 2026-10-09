// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/forgecred"
	"github.com/spf13/cobra"
)

const gitCredTestToken = "ghu_FAKETOKEN0123456789"

func runGitCredential(t *testing.T, fetch gitTokenFetcher, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := newAuthGitCredentialCmdWith(fetch)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

func okFetch(calls *int) gitTokenFetcher {
	return func(context.Context, *cobra.Command, forgecred.Pin, string) (string, error) {
		*calls++
		return gitCredTestToken, nil
	}
}

func TestGitCredential_AnswersGithubHTTPSOnly(t *testing.T) {
	calls := 0
	out, _, err := runGitCredential(t, okFetch(&calls), "protocol=https\nhost=github.com\n\n", "get")
	if err != nil {
		t.Fatal(err)
	}
	if out != "username=x-access-token\npassword="+gitCredTestToken+"\n" {
		t.Fatalf("out = %q", out)
	}
	for name, in := range map[string]string{
		"other host": "protocol=https\nhost=example.com\n\n",
		"look-alike": "protocol=https\nhost=github.com.evil.io\n\n",
		"http":       "protocol=http\nhost=github.com\n\n",
		"no host":    "protocol=https\n\n",
	} {
		calls = 0
		out, _, err := runGitCredential(t, okFetch(&calls), in, "get")
		if err != nil || out != "" || calls != 0 {
			t.Errorf("%s: out=%q err=%v calls=%d; want silence and no RPC", name, out, err, calls)
		}
	}
}

func TestGitCredential_StoreAndEraseAreNoOps(t *testing.T) {
	calls := 0
	for _, op := range []string{"store", "erase"} {
		out, errs, err := runGitCredential(t, okFetch(&calls), "protocol=https\nhost=github.com\nusername=x-access-token\npassword="+gitCredTestToken+"\n\n", op)
		if err != nil || out != "" || errs != "" || calls != 0 {
			t.Errorf("%s: out=%q err=%q %v calls=%d", op, out, errs, err, calls)
		}
	}
}

func TestGitCredential_PermanentErrorFailsWithoutToken(t *testing.T) {
	fetch := func(context.Context, *cobra.Command, forgecred.Pin, string) (string, error) {
		return "", errGitProviderNotConnected
	}
	out, errs, err := runGitCredential(t, fetch, "protocol=https\nhost=github.com\n\n", "get")
	if err == nil || out != "" {
		t.Fatalf("out=%q err=%v; want non-zero and empty stdout", out, err)
	}
	if !strings.Contains(err.Error(), "reconnect GitHub") || strings.Contains(err.Error()+errs, gitCredTestToken) {
		t.Fatalf("err = %v", err)
	}
	fetch = func(context.Context, *cobra.Command, forgecred.Pin, string) (string, error) {
		return "", errors.New("boom")
	}
	if out, _, err := runGitCredential(t, fetch, "protocol=https\nhost=github.com\n\n", "get"); err == nil || out != "" {
		t.Fatalf("transient error must also fail with empty stdout: %q %v", out, err)
	}
}

type fakeGitTokenService struct {
	reliantv1connect.UnimplementedTokenServiceHandler
	bearer, provider string
	err              error
}

func (f *fakeGitTokenService) GetGitToken(_ context.Context, r *connect.Request[reliantv1.GetGitTokenRequest]) (*connect.Response[reliantv1.GetGitTokenResponse], error) {
	f.bearer = r.Header().Get("Authorization")
	f.provider = r.Msg.GetProvider()
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&reliantv1.GetGitTokenResponse{AccessToken: gitCredTestToken}), nil
}

func TestFetchGitToken_CallsServerAsPinnedDaemonSession(t *testing.T) {
	isolateForgeCredHome(t)
	fake := &fakeGitTokenService{}
	mux := http.NewServeMux()
	path, h := reliantv1connect.NewTokenServiceHandler(fake)
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if err := auth.WriteDaemonCredentials(&auth.DaemonCredentials{PAT: "rlat_daemon", ServerURL: srv.URL}); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	tok, err := fetchGitToken(context.Background(), cmd, forgecred.Pin{Server: srv.URL}, "github")
	if err != nil || tok != gitCredTestToken {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	if fake.bearer != "Bearer rlat_daemon" || fake.provider != "github" {
		t.Fatalf("bearer=%q provider=%q", fake.bearer, fake.provider)
	}

	fake.err = connect.NewError(connect.CodeFailedPrecondition, errors.New("not connected"))
	if _, err := fetchGitToken(context.Background(), cmd, forgecred.Pin{Server: srv.URL}, "github"); !errors.Is(err, errGitProviderNotConnected) {
		t.Fatalf("err = %v, want errGitProviderNotConnected", err)
	}
}
