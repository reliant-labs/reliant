// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecoveryProofDoesNotInheritSourceObjects(t *testing.T) {
	f, c, tr := recoveryFixture(t)
	pubGit(t, f.clone, "push", f.remote, c.CommitOID+":"+c.RecoveryRef)
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(f.clone, ".git", "objects"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(f.clone, ".git", "objects"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file:///bad/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/")
	t.Setenv("GIT_TRACE", filepath.Join(t.TempDir(), "trace"))
	t.Setenv("HTTPS_PROXY", "http://untrusted.invalid")
	t.Setenv("GH_TOKEN", "DAEMON_TOKEN_NOT_FOR_GIT")
	run := tr.run
	tr.run = func(ctx context.Context, dir string, env []string, stdin string, index bool, extra []*os.File, args ...string) (pubGitResult, error) {
		for _, kv := range env {
			require.NotContains(t, kv, "DAEMON_TOKEN_NOT_FOR_GIT")
			require.NotContains(t, kv, "untrusted.invalid")
			require.False(t, strings.HasPrefix(kv, "GIT_OBJECT_DIRECTORY="))
			require.False(t, strings.HasPrefix(kv, "GIT_ALTERNATE_OBJECT_DIRECTORIES="))
			require.False(t, strings.HasPrefix(kv, "GIT_TRACE="))
			require.False(t, strings.HasPrefix(kv, "GIT_CONFIG_COUNT="))
		}
		require.NotContains(t, strings.Join(args, " "), publishToken)
		return run(ctx, dir, env, stdin, index, extra, args...)
	}
	s, err := tr.session(c, publishToken)
	require.NoError(t, err)
	defer s.close()
	_, err = s.verify(context.Background(), c, nil)
	require.NoError(t, err)
}
func TestRecoveryIncompleteRemoteClosureHeld(t *testing.T) {
	t.Parallel()
	f, c, tr := recoveryFixture(t)
	pubGit(t, f.clone, "push", f.remote, c.CommitOID+":"+c.RecoveryRef)
	blob := pubGit(t, f.remote, "rev-parse", c.CommitOID+":README.md")
	// Ordinary bare fixtures keep loose objects; remove a remote-only blob while
	// source remains intact. The verifier must not borrow it from the source.
	path := filepath.Join(f.remote, "objects", blob[:2], blob[2:])
	require.NoError(t, os.Remove(path))
	s, err := tr.session(c, publishToken)
	require.NoError(t, err)
	defer s.close()
	_, err = s.verify(context.Background(), c, nil)
	require.Error(t, err)
}
func TestRecoveryOutputBounded(t *testing.T) {
	var b recoveryOutput
	_, err := b.Write([]byte(strings.Repeat("a", 1<<20)))
	require.NoError(t, err)
	_, err = b.Write([]byte("b"))
	require.Error(t, err)
	require.True(t, b.overflow)
	require.Equal(t, 1<<20, b.Len())
}
