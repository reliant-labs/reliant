// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	forgecli "github.com/reliant-labs/forge/cli"
	"github.com/reliant-labs/reliant/internal/forgecred"
)

const ghBotTestToken = "ghs_FAKEBOTTOKEN0123456789"

type recordedGitHub struct {
	reqs   []githubTokenRequest
	ghArgs []string
	ghEnv  []string
}

func (r *recordedGitHub) fetch(err error) githubTokenFetcher {
	return func(_ context.Context, _ *cobra.Command, _ forgecred.Pin, req githubTokenRequest) (githubBotToken, error) {
		r.reqs = append(r.reqs, req)
		if err != nil {
			return githubBotToken{}, err
		}
		return githubBotToken{Token: ghBotTestToken, BotLogin: "reliant-labs[bot]", Repository: req.Owner + "/" + req.Repo,
			ExpiresAt: "2030-01-02T03:04:05Z"}, nil
	}
}

func (r *recordedGitHub) run(code int) ghRunner {
	return func(_ *cobra.Command, args, env []string) (int, error) {
		r.ghArgs, r.ghEnv = args, env
		return code, nil
	}
}

func runGitHubCmd(t *testing.T, fetch githubTokenFetcher, run ghRunner, args ...string) (string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := newGitHubCmdWith(fetch, run)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestGitHubToken_AppIsScopedToOneRepository(t *testing.T) {
	var rec recordedGitHub
	out, err := runGitHubCmd(t, rec.fetch(nil), rec.run(0), "token", "--repo", "reliant-labs/forge",
		"--permission", "pull_requests=write", "--permission", "issues=read")
	if err != nil {
		t.Fatal(err)
	}
	if out != ghBotTestToken+"\n" {
		t.Errorf("stdout = %q, want the token alone", out)
	}
	want := githubTokenRequest{As: "app", Owner: "reliant-labs", Repo: "forge",
		Permissions: map[string]string{"pull_requests": "write", "issues": "read"}}
	if len(rec.reqs) != 1 || rec.reqs[0].As != want.As || rec.reqs[0].Owner != want.Owner || rec.reqs[0].Repo != want.Repo ||
		len(rec.reqs[0].Permissions) != 2 || rec.reqs[0].Permissions["issues"] != "read" {
		t.Errorf("requested %+v, want %+v", rec.reqs, want)
	}
}

func TestGitHubToken_JSONSaysWhoItSpeaksAs(t *testing.T) {
	var rec recordedGitHub
	out, err := runGitHubCmd(t, rec.fetch(nil), rec.run(0), "token", "--as", "app", "--repo", "acme/api", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got githubBotToken
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	if got.Token != ghBotTestToken || got.BotLogin != "reliant-labs[bot]" || got.Repository != "acme/api" || got.ExpiresAt == "" {
		t.Errorf("json = %+v", got)
	}
}

func TestGitHubToken_RefusesWhatItCannotScope(t *testing.T) {
	for name, args := range map[string][]string{
		"app without repo":       {"token"},
		"repo without owner":     {"token", "--repo", "forge"},
		"nested repo":            {"token", "--repo", "a/b/c"},
		"bad permission":         {"token", "--repo", "a/b", "--permission", "pull_requests"},
		"unknown identity":       {"token", "--as", "admin", "--repo", "a/b"},
		"user with a repo scope": {"token", "--as", "user", "--repo", "a/b"},
	} {
		t.Run(name, func(t *testing.T) {
			var rec recordedGitHub
			if _, err := runGitHubCmd(t, rec.fetch(nil), rec.run(0), args...); err == nil {
				t.Fatal("want an error")
			}
			if len(rec.reqs) != 0 {
				t.Errorf("fetched a token: %+v", rec.reqs)
			}
		})
	}
}

func TestGitHubToken_UserAsksForTheUsersToken(t *testing.T) {
	var rec recordedGitHub
	if _, err := runGitHubCmd(t, rec.fetch(nil), rec.run(0), "token", "--as", "user"); err != nil {
		t.Fatal(err)
	}
	if len(rec.reqs) != 1 || rec.reqs[0].As != "user" || rec.reqs[0].Owner != "" {
		t.Errorf("requested %+v", rec.reqs)
	}
}

func TestGitHubToken_FetchErrorPrintsNoToken(t *testing.T) {
	var rec recordedGitHub
	out, err := runGitHubCmd(t, rec.fetch(errors.New("you cannot push to acme/api")), rec.run(0), "token", "--repo", "acme/api")
	if err == nil || !strings.Contains(err.Error(), "cannot push") || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestGitHubGH_RunsGHAsTheBotInTheRepository(t *testing.T) {
	var rec recordedGitHub
	t.Setenv("GH_TOKEN", "gho_OWNERTOKEN")
	t.Setenv("GITHUB_TOKEN", "gho_OWNERTOKEN")
	t.Setenv("GH_HOST", "example.com")
	t.Setenv("GH_REPO", "someone/else")
	_, err := runGitHubCmd(t, rec.fetch(nil), rec.run(0), "gh", "--repo", "reliant-labs/reliant", "--",
		"pr", "merge", "12", "--squash", "--auto", "--match-head-commit", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rec.ghArgs, []string{"pr", "merge", "12", "--squash", "--auto", "--match-head-commit", "abc"}) {
		t.Errorf("gh args = %q", rec.ghArgs)
	}
	env := strings.Join(rec.ghEnv, "\n")
	if strings.Contains(env, "gho_OWNERTOKEN") || strings.Contains(env, "GH_HOST=") || strings.Contains(env, "someone/else") {
		t.Errorf("gh could still act as another identity or repository:\n%s", env)
	}
	if !slices.Contains(rec.ghEnv, "GH_TOKEN="+ghBotTestToken) || !slices.Contains(rec.ghEnv, "GH_REPO=reliant-labs/reliant") {
		t.Errorf("gh env lacks the bot token or its repository")
	}
}

func TestGitHubGH_PassesGHsExitCode(t *testing.T) {
	var rec recordedGitHub
	_, err := runGitHubCmd(t, rec.fetch(nil), rec.run(4), "gh", "--repo", "a/b", "--", "pr", "view", "1")
	if got := forgecli.ExitCode(err); got != 4 {
		t.Fatalf("exit code = %d (err %v), want gh's 4", got, err)
	}
}

func TestGitHubGH_NeedsTheDash(t *testing.T) {
	var rec recordedGitHub
	if _, err := runGitHubCmd(t, rec.fetch(nil), rec.run(0), "gh", "--repo", "a/b", "pr", "view", "1"); err == nil {
		t.Fatal("want an error when gh's args are not after --")
	}
	if len(rec.reqs) != 0 || rec.ghArgs != nil {
		t.Error("minted or ran without --")
	}
}

func TestGitHubGH_FetchErrorRunsNothing(t *testing.T) {
	var rec recordedGitHub
	if _, err := runGitHubCmd(t, rec.fetch(errors.New("not installed")), rec.run(0), "gh", "--repo", "a/b", "--", "pr", "view", "1"); err == nil {
		t.Fatal("want an error")
	}
	if rec.ghArgs != nil {
		t.Error("ran gh without a token")
	}
}
