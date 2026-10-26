// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
	"github.com/stretchr/testify/require"
)

func TestRecoveryGitHubIdentity(t *testing.T) {
	for _, raw := range []string{"file:///tmp/r.git", "/tmp/r.git", "http://github.com/a/b.git", "https://github.com:443/a/b.git", "https://github.com.evil/a/b.git", "https://u:p@github.com/a/b.git", "https://github.com/a/b.git?q=x", "https://github.com/a/b.git#x", "https://github.com/a/%62.git", "https://github.com/a/../b.git", "https://github.com/a/.git", "https://github.com/a/b.git/", "https://github.com/a/b"} {
		_, err := recoveryGitHubURL(raw)
		require.Error(t, err, raw)
	}
	_, err := recoveryGitHubURL("https://github.com/reliant-labs/reliant.git")
	require.NoError(t, err)
}
func TestRecoveryCredentialInputs(t *testing.T) {
	for _, token := range []string{"", "abc\nsecret", "abc\x00secret", strings.Repeat("x", 4097)} {
		_, err := credentialPipe(token)
		require.Error(t, err)
	}
	args := strings.Join(privateGitCredentialArgs("github.com", "owner/repo.git"), " ")
	require.Contains(t, args, `[ "$s" = https ]`)
	require.Contains(t, args, `[ "$p" = "owner/repo.git" ]`)
	require.Contains(t, args, "http.followRedirects=false")
	require.NotContains(t, args, publishToken)
}

func recoveryFixture(t *testing.T) (publishFixture, worktreereclaim.RecoveryCapture, recoveryTransport) {
	t.Helper()
	needGit(t)
	f := newPublishFixture(t)
	c := worktreereclaim.RecoveryCapture{Version: 1, WorktreeID: "a123", CaptureID: "b123", RepoID: "c123", RemoteURL: "https://github.com/owner/repo.git", ObjectFormat: "sha1", CommitOID: pubGit(t, f.clone, "rev-parse", "HEAD")}
	c.RecoveryRef = "refs/heads/reliant-recovery/" + c.WorktreeID + "/" + c.CaptureID + "/" + c.RepoID
	tr := productionRecoveryTransport()
	tr.endpoint = func(string) (*url.URL, error) { return url.Parse("file://" + f.remote) }
	run := tr.run
	tr.run = func(ctx context.Context, dir string, env []string, stdin string, index bool, extra []*os.File, args ...string) (pubGitResult, error) {
		// Test-only injection. Production has no file protocol escape hatch.
		args = append([]string{"-c", "protocol.file.allow=always"}, args...)
		return run(ctx, dir, env, stdin, index, extra, args...)
	}
	tr.validate = func(ctx context.Context, dir string, c worktreereclaim.RecoveryCapture) error {
		got, err := pubRunGit(ctx, dir, isolatedGitEnv(), "", false, nil, "rev-parse", "--verify", c.CommitOID+"^{commit}")
		if err != nil || got.exit != 0 {
			return errors.New("missing fixture commit")
		}
		return nil
	}
	return f, c, tr
}
func TestRecoveryCASVerifyAndExplicitDelete(t *testing.T) {
	t.Parallel()
	f, c, tr := recoveryFixture(t)
	before := pubGit(t, f.clone, "status", "--porcelain=v1")
	receipt, err := tr.publish(context.Background(), f.clone, c, publishToken)
	require.NoError(t, err)
	require.Equal(t, c, receipt.Capture)
	require.False(t, receipt.VerifiedAt.IsZero())
	_, err = tr.publish(context.Background(), f.clone, c, publishToken)
	require.NoError(t, err, "same exact create is idempotent")
	require.Equal(t, c.CommitOID, pubGit(t, f.remote, "rev-parse", c.RecoveryRef))
	require.Equal(t, before, pubGit(t, f.clone, "status", "--porcelain=v1"))
	require.Equal(t, c.CommitOID, pubGit(t, f.clone, "rev-parse", "HEAD"))
	require.NoError(t, os.RemoveAll(f.clone))
	s, err := tr.session(c, publishToken)
	require.NoError(t, err)
	defer s.close()
	_, err = s.verify(context.Background(), c, func(bare string) error {
		require.NotEqual(t, f.clone, bare)
		_, e := os.Stat(filepath.Join(bare, "objects", "info", "alternates"))
		require.True(t, os.IsNotExist(e))
		require.Equal(t, c.CommitOID, pubGit(t, bare, "rev-parse", "refs/heads/proof"))
		require.Empty(t, pubGit(t, bare, "fsck", "--full", "--strict", c.CommitOID))
		return nil
	})
	require.NoError(t, err, "proof survives complete source deletion")
	require.NoError(t, tr.delete(context.Background(), c, publishToken))
	require.NoError(t, tr.delete(context.Background(), c, publishToken), "lost delete response retries absent")
	require.Empty(t, pubGit(t, f.remote, "for-each-ref", "--format=%(refname)", c.RecoveryRef))
	require.NotEmpty(t, pubGit(t, f.remote, "rev-parse", "main"), "ordinary branches untouched")
}
func TestRecoveryConflictsNeverOverwriteOrDelete(t *testing.T) {
	t.Parallel()
	f, c, tr := recoveryFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "next"), []byte("next"), 0600))
	pubGit(t, f.clone, "add", "next")
	pubGit(t, f.clone, "commit", "-qm", "next")
	other := pubGit(t, f.clone, "rev-parse", "HEAD")
	pubGit(t, f.clone, "push", f.remote, other+":"+c.RecoveryRef)
	_, err := tr.publish(context.Background(), f.clone, c, publishToken)
	require.ErrorContains(t, err, "conflict")
	require.ErrorContains(t, tr.delete(context.Background(), c, publishToken), "changed")
	require.Equal(t, other, pubGit(t, f.remote, "rev-parse", c.RecoveryRef))
}
func TestRecoveryConcurrentCreateAndDeleteUseExactLease(t *testing.T) {
	t.Parallel()
	f, c, tr := recoveryFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "next"), []byte("next"), 0600))
	pubGit(t, f.clone, "add", "next")
	pubGit(t, f.clone, "commit", "-qm", "next")
	other := pubGit(t, f.clone, "rev-parse", "HEAD")
	run := tr.run
	tr.run = func(ctx context.Context, dir string, env []string, stdin string, index bool, extra []*os.File, args ...string) (pubGitResult, error) {
		for _, arg := range args {
			if arg == "--force-with-lease="+c.RecoveryRef+":" {
				pubGit(t, f.clone, "push", f.remote, other+":"+c.RecoveryRef)
			}
		}
		return run(ctx, dir, env, stdin, index, extra, args...)
	}
	_, err := tr.publish(context.Background(), f.clone, c, publishToken)
	require.Error(t, err)
	require.Equal(t, other, pubGit(t, f.remote, "rev-parse", c.RecoveryRef))
	pubGit(t, f.remote, "update-ref", c.RecoveryRef, c.CommitOID)
	tr.run = func(ctx context.Context, dir string, env []string, stdin string, index bool, extra []*os.File, args ...string) (pubGitResult, error) {
		for _, arg := range args {
			if arg == "--force-with-lease="+c.RecoveryRef+":"+c.CommitOID {
				pubGit(t, f.remote, "update-ref", c.RecoveryRef, other)
			}
		}
		return run(ctx, dir, env, stdin, index, extra, args...)
	}
	require.Error(t, tr.delete(context.Background(), c, publishToken))
	require.Equal(t, other, pubGit(t, f.remote, "rev-parse", c.RecoveryRef))
}
func TestRecoveryFakePushAndWrongProofRefHeld(t *testing.T) {
	t.Parallel()
	f, c, tr := recoveryFixture(t)
	run := tr.run
	tr.run = func(ctx context.Context, dir string, env []string, stdin string, index bool, extra []*os.File, args ...string) (pubGitResult, error) {
		for _, a := range args {
			if a == "push" {
				return pubGitResult{}, nil
			}
		}
		return run(ctx, dir, env, stdin, index, extra, args...)
	}
	_, err := tr.publish(context.Background(), f.clone, c, publishToken)
	require.Error(t, err, "fake push acceptance is not a receipt")
	tr.run = run
	pubGit(t, f.clone, "push", f.remote, c.CommitOID+":"+c.RecoveryRef)
	expected := c
	c.CommitOID = strings.Repeat("1", 40)
	s, err := tr.session(c, publishToken)
	require.NoError(t, err)
	defer s.close()
	_, err = s.verify(context.Background(), c, nil)
	require.ErrorContains(t, err, "differs")
	tr.validate = func(context.Context, string, worktreereclaim.RecoveryCapture) error {
		return errors.New("manifest invalid")
	}
	s2, err := tr.session(expected, publishToken)
	require.NoError(t, err)
	defer s2.close()
	_, err = s2.verify(context.Background(), expected, nil)
	require.ErrorContains(t, err, "manifest")
}
func TestRecoveryPreEgressGateAndConfigHold(t *testing.T) {
	t.Parallel()
	f, c, tr := recoveryFixture(t)
	calls := 0
	run := tr.run
	tr.run = func(ctx context.Context, dir string, env []string, stdin string, index bool, extra []*os.File, args ...string) (pubGitResult, error) {
		if len(extra) > 0 {
			calls++
		}
		return run(ctx, dir, env, stdin, index, extra, args...)
	}
	validate := tr.validate
	tr.validate = func(context.Context, string, worktreereclaim.RecoveryCapture) error {
		return errors.New("secret or unsupported")
	}
	_, err := tr.publish(context.Background(), f.clone, c, publishToken)
	require.ErrorContains(t, err, "pre-egress")
	require.Zero(t, calls)
	tr.validate = validate
	for _, key := range []string{"credential.helper", "http.https://github.com/.extraHeader", "url.https://evil/.insteadOf"} {
		pubGit(t, f.clone, "config", key, "malicious")
		_, err = tr.publish(context.Background(), f.clone, c, publishToken)
		require.ErrorContains(t, err, "configuration")
		pubGit(t, f.clone, "config", "--unset-all", key)
	}
	require.Zero(t, calls)
}
func TestRecoveryHTTPAuthenticationAllOperations(t *testing.T) {
	t.Parallel()
	f, c, tr := recoveryFixture(t)
	srv := newSmartHTTP(t, f.remote)
	tr.endpoint = func(string) (*url.URL, error) { return url.Parse(srv.URL + "/remote.git") }
	// The fixture endpoint is HTTP; only its injected runner relaxes protocol
	// and HTTPS helper guard. Production never permits either relaxation.
	baseRun := tr.run
	tr.run = func(ctx context.Context, dir string, env []string, stdin string, index bool, extra []*os.File, args ...string) (pubGitResult, error) {
		for i, a := range args {
			args[i] = strings.ReplaceAll(a, `[ "$s" = https ]`, `[ "$s" = http ]`)
		}
		args = append([]string{"-c", "protocol.http.allow=always"}, args...)
		return baseRun(ctx, dir, env, stdin, index, extra, args...)
	}
	_, err := tr.publish(context.Background(), f.clone, c, publishToken)
	require.NoError(t, err)
	require.Positive(t, srv.authenticated())
	require.NoError(t, tr.delete(context.Background(), c, publishToken))
	_, err = tr.publish(context.Background(), f.clone, c, "invalid-token")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "invalid-token")
}
func TestRecoveryCommandErrorsNeverEchoPayload(t *testing.T) {
	payload, _ := json.Marshal(GitRecoveryRequest{Token: publishToken, Capture: worktreereclaim.RecoveryCapture{RemoteURL: publishToken}})
	raw, err := handleGitRecoveryPublish(context.Background(), payload)
	require.NoError(t, err)
	require.NotContains(t, string(raw), publishToken)
	var out GitRecoveryResponse
	require.NoError(t, json.Unmarshal(raw, &out))
	require.True(t, out.Held)
	require.Nil(t, out.Receipt)
	_, err = handleGitRecoveryPublish(context.Background(), []byte(`{"token":"`+publishToken))
	require.Error(t, err)
	require.NotContains(t, err.Error(), publishToken)
}
