// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// newPolicyGateClient returns a daemon client wired with buffered channels, so
// handleDaemonCommand can run inline and its response be read back.
func newPolicyGateClient(t *testing.T) *daemonClient {
	t.Helper()
	d := newTestDaemonClient("daemon-policy", "user-policy")
	d.sendCh = make(chan *reliantv1.DaemonMessage, 4)
	d.sessionDone = make(chan struct{})
	return d
}

// runCommand dispatches req and returns the response the daemon sent back.
func runCommand(t *testing.T, d *daemonClient, req *reliantv1.DaemonCommandRequest) *reliantv1.DaemonCommandResponse {
	t.Helper()
	d.handleDaemonCommand(req)

	select {
	case msg := <-d.sendCh:
		resp := msg.GetDaemonCommandResponse()
		require.NotNil(t, resp, "expected a daemon command response")
		return resp
	case <-time.After(5 * time.Second):
		t.Fatal("daemon sent no response")
		return nil
	}
}

func mustPayload(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// TestPolicyGate_FirstPartyUnaffected is the regression guard for existing
// behavior: a request with no policy must execute exactly as it did before the
// gate existed.
func TestPolicyGate_FirstPartyUnaffected(t *testing.T) {
	d := newPolicyGateClient(t)

	dir := t.TempDir()
	target := filepath.Join(dir, "hello.txt")
	require.NoError(t, os.WriteFile(target, []byte("hi"), 0o600))

	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "req-1",
		CommandType: "fs.read_file",
		Payload:     mustPayload(t, map[string]string{"path": target}),
		// Policy intentionally unset — the first-party path.
	})

	require.True(t, resp.GetSuccess(), "unrestricted request failed: %s", resp.GetErrorMessage())
}

// TestPolicyGate_DeniesUngrantedCommand proves the gate refuses before the
// handler runs.
func TestPolicyGate_DeniesUngrantedCommand(t *testing.T) {
	d := newPolicyGateClient(t)
	dir := t.TempDir()

	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "req-2",
		CommandType: "fs.delete",
		Payload:     mustPayload(t, map[string]string{"path": filepath.Join(dir, "x.txt")}),
		Policy: &reliantv1.ConnectorPolicy{
			GrantId:      "grant-1",
			AllowedTools: []string{"fs.read_file"},
			PathRoot:     dir,
		},
	})

	require.False(t, resp.GetSuccess())
	require.Contains(t, resp.GetErrorMessage(), "not in this connector's allowed tools")
}

// TestPolicyGate_ConfinesPaths proves a granted command still cannot reach
// outside its root — the case that matters most for a connector.
func TestPolicyGate_ConfinesPaths(t *testing.T) {
	d := newPolicyGateClient(t)

	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("sensitive"), 0o600))

	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "req-3",
		CommandType: "fs.read_file",
		Payload:     mustPayload(t, map[string]string{"path": secret}),
		Policy: &reliantv1.ConnectorPolicy{
			GrantId:      "grant-1",
			AllowedTools: []string{"fs.read_file"},
			PathRoot:     root,
		},
	})

	require.False(t, resp.GetSuccess())
	require.Contains(t, resp.GetErrorMessage(), "outside this connector's allowed directory")

	// The denial must not leak the file's contents through the payload.
	require.NotContains(t, string(resp.GetPayload()), "sensitive")
}

// TestPolicyGate_AllowsGrantedPathAndCommand confirms the gate is not simply
// refusing everything.
func TestPolicyGate_AllowsGrantedPathAndCommand(t *testing.T) {
	d := newPolicyGateClient(t)

	root := t.TempDir()
	target := filepath.Join(root, "readme.md")
	require.NoError(t, os.WriteFile(target, []byte("# hello"), 0o600))

	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "req-4",
		CommandType: "fs.read_file",
		Payload:     mustPayload(t, map[string]string{"path": target}),
		Policy: &reliantv1.ConnectorPolicy{
			GrantId:      "grant-1",
			AllowedTools: []string{"fs.read_file"},
			PathRoot:     root,
		},
	})

	require.True(t, resp.GetSuccess(), "granted request failed: %s", resp.GetErrorMessage())
	require.Contains(t, string(resp.GetPayload()), "hello")
}

// TestPolicyGate_ExecAllowlist checks shell confinement end to end.
func TestPolicyGate_ExecAllowlist(t *testing.T) {
	root := t.TempDir()

	basePolicy := func() *reliantv1.ConnectorPolicy {
		return &reliantv1.ConnectorPolicy{
			GrantId:       "grant-exec",
			AllowedTools:  []string{"exec.run"},
			PathRoot:      root,
			ExecMode:      "allowlist",
			ExecAllowlist: []string{"echo"},
		}
	}

	t.Run("allowlisted command runs", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "req-5",
			CommandType: "exec.run",
			Payload: mustPayload(t, map[string]any{
				"argv":        []string{"echo", "policy-ok"},
				"working_dir": root,
			}),
			Policy: basePolicy(),
		})
		require.True(t, resp.GetSuccess(), "allowlisted command failed: %s", resp.GetErrorMessage())
		require.Contains(t, string(resp.GetPayload()), "policy-ok")
	})

	t.Run("unlisted program is refused", func(t *testing.T) {
		d := newPolicyGateClient(t)
		marker := filepath.Join(root, "should-not-exist.txt")
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "req-6",
			CommandType: "exec.run",
			Payload: mustPayload(t, map[string]any{
				// The classic escape: hand a shell the whole string. With an
				// allowlist there is no shell to hand it to, and "sh" is not
				// a permitted program.
				"argv":        []string{"sh", "-c", "echo hi > " + marker},
				"working_dir": root,
			}),
			Policy: basePolicy(),
		})

		require.False(t, resp.GetSuccess())
		require.Contains(t, resp.GetErrorMessage(), "not in this connector's allowed commands")

		// The refusal must have happened before execution, not after.
		_, err := os.Stat(marker)
		require.True(t, os.IsNotExist(err), "refused command still wrote to disk")
	})

	t.Run("exec denied when not granted", func(t *testing.T) {
		d := newPolicyGateClient(t)
		p := basePolicy()
		p.ExecMode = ""
		p.ExecAllowlist = nil

		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "req-7",
			CommandType: "exec.run",
			Payload:     mustPayload(t, map[string]any{"argv": []string{"echo", "hi"}, "working_dir": root}),
			Policy:      p,
		})
		require.False(t, resp.GetSuccess())
		require.Contains(t, resp.GetErrorMessage(), "shell execution is not granted")
	})
}

// TestPolicyGate_ArgvBypassesTheShell is the end-to-end proof that an exec
// allowlist is a real boundary rather than a string check racing an
// interpreter.
//
// Each case sends shell syntax through argv and asserts it was NOT
// interpreted: the metacharacters arrive as literal arguments to the program,
// so nothing chains, nothing expands, and nothing is written.
func TestPolicyGate_ArgvBypassesTheShell(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "planted.txt")

	policy := func() *reliantv1.ConnectorPolicy {
		return &reliantv1.ConnectorPolicy{
			GrantId:       "grant-argv",
			AllowedTools:  []string{"exec.run"},
			PathRoot:      root,
			ExecMode:      "allowlist",
			ExecAllowlist: []string{"echo"},
		}
	}

	t.Run("metacharacters are literal arguments", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "argv-1",
			CommandType: "exec.run",
			Payload: mustPayload(t, map[string]any{
				// Under `bash -c` this would redirect into the file. Through
				// argv it is text that echo prints.
				"argv":        []string{"echo", "hello > " + marker},
				"working_dir": root,
			}),
			Policy: policy(),
		})

		require.True(t, resp.GetSuccess(), "argv command failed: %s", resp.GetErrorMessage())

		// Decode rather than substring-matching the raw JSON: the response
		// escapes '>' as \u003e, which would make a naive match fail against
		// output that is in fact correct.
		var result struct {
			Stdout string `json:"stdout"`
		}
		require.NoError(t, json.Unmarshal(resp.GetPayload(), &result))
		require.Contains(t, result.Stdout, "hello > ",
			"the redirection should have been printed as text, not performed")

		_, err := os.Stat(marker)
		require.True(t, os.IsNotExist(err), "argv execution must not have invoked a shell")
	})

	t.Run("command substitution is not evaluated", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "argv-2",
			CommandType: "exec.run",
			Payload: mustPayload(t, map[string]any{
				"argv":        []string{"echo", "$(whoami)"},
				"working_dir": root,
			}),
			Policy: policy(),
		})

		require.True(t, resp.GetSuccess(), "argv command failed: %s", resp.GetErrorMessage())
		require.Contains(t, string(resp.GetPayload()), "$(whoami)",
			"command substitution must survive as literal text")
	})

	t.Run("a shell string is refused under an allowlist", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "argv-3",
			CommandType: "exec.run",
			Payload: mustPayload(t, map[string]any{
				"command":     "echo hi",
				"working_dir": root,
			}),
			Policy: policy(),
		})

		require.False(t, resp.GetSuccess())
		require.Contains(t, resp.GetErrorMessage(), "separately")
	})

	t.Run("planted binary on PATH is refused", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "argv-4",
			CommandType: "exec.run",
			Payload: mustPayload(t, map[string]any{
				"argv":        []string{"echo", "hi"},
				"working_dir": root,
				"env":         map[string]string{"PATH": "/tmp/planted"},
			}),
			Policy: policy(),
		})

		require.False(t, resp.GetSuccess(), "overriding PATH must be refused")
		require.Contains(t, resp.GetErrorMessage(), "PATH")
	})
}

// TestPolicyGate_HandlerReresolvesPaths proves the handler itself re-checks,
// not only the dispatch gate.
//
// This matters because the dispatch check inspects argument NAMES and so
// cannot cover every payload shape, and because a path that was in bounds when
// checked can be swapped for an escaping symlink before it is opened. The
// handler resolving through os.Root immediately before use closes both.
func TestPolicyGate_HandlerReresolvesPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on Windows")
	}

	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("sensitive"), 0o600))

	// A directory inside the root that will be swapped for a symlink out.
	victim := filepath.Join(root, "subdir")
	require.NoError(t, os.Mkdir(victim, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(victim, "secret.txt"), []byte("harmless"), 0o600))

	policy := &reliantv1.ConnectorPolicy{
		GrantId:      "grant-toctou",
		AllowedTools: []string{"fs.read_file"},
		PathRoot:     root,
	}
	target := filepath.Join(victim, "secret.txt")

	// In bounds to begin with.
	d := newPolicyGateClient(t)
	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "toctou-1",
		CommandType: "fs.read_file",
		Payload:     mustPayload(t, map[string]string{"path": target}),
		Policy:      policy,
	})
	require.True(t, resp.GetSuccess(), "in-bounds read failed: %s", resp.GetErrorMessage())
	require.Contains(t, string(resp.GetPayload()), "harmless")

	// Swap the directory for a symlink pointing outside the root.
	require.NoError(t, os.RemoveAll(victim))
	require.NoError(t, os.Symlink(outside, victim))

	// The identical request must now be refused, and must not leak the file.
	d2 := newPolicyGateClient(t)
	resp2 := runCommand(t, d2, &reliantv1.DaemonCommandRequest{
		RequestId:   "toctou-2",
		CommandType: "fs.read_file",
		Payload:     mustPayload(t, map[string]string{"path": target}),
		Policy:      policy,
	})
	require.False(t, resp2.GetSuccess(), "a path swapped for an escaping symlink must be refused")
	require.NotContains(t, string(resp2.GetPayload()), "sensitive")
}

// TestPolicyGate_ExpiredGrant confirms expiry is enforced at dispatch.
func TestPolicyGate_ExpiredGrant(t *testing.T) {
	d := newPolicyGateClient(t)
	root := t.TempDir()

	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "req-8",
		CommandType: "fs.read_file",
		Payload:     mustPayload(t, map[string]string{"path": filepath.Join(root, "a.txt")}),
		Policy: &reliantv1.ConnectorPolicy{
			GrantId:      "grant-expired",
			AllowedTools: []string{"fs.read_file"},
			PathRoot:     root,
			ExpiresAt:    time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		},
	})

	require.False(t, resp.GetSuccess())
	require.Contains(t, resp.GetErrorMessage(), "expired")
}

// TestPolicyGate_ConfinesGetTree covers the one fs command that had no policy
// gate at all: handleFSGetTree discarded its context, so it could not consult
// the policy, and an absent path sent it walking the daemon's own working
// directory — which is never inside a grant, because the daemon is not chdir'd
// into one.
//
// fs.get_tree is the file browser's own command, so an unconfined walk here
// hands a connector a map of the whole machine.
func TestPolicyGate_ConfinesGetTree(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("sensitive"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "inside.txt"), []byte("ok"), 0o600))

	policy := func() *reliantv1.ConnectorPolicy {
		return &reliantv1.ConnectorPolicy{
			GrantId:      "grant-tree",
			AllowedTools: []string{"fs.get_tree"},
			PathRoot:     root,
		}
	}

	t.Run("an absolute path outside the root is refused", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "tree-1",
			CommandType: "fs.get_tree",
			Payload:     mustPayload(t, map[string]any{"path": outside}),
			Policy:      policy(),
		})
		require.False(t, resp.GetSuccess())
		require.Contains(t, resp.GetErrorMessage(), "outside")
		require.NotContains(t, string(resp.GetPayload()), "secret.txt")
	})

	t.Run("a relative traversal is refused", func(t *testing.T) {
		for _, escape := range []string{"..", "../..", "../" + filepath.Base(outside)} {
			d := newPolicyGateClient(t)
			resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
				RequestId:   "tree-2",
				CommandType: "fs.get_tree",
				Payload:     mustPayload(t, map[string]any{"path": escape}),
				Policy:      policy(),
			})
			require.False(t, resp.GetSuccess(), "escape %q was permitted", escape)
			require.Contains(t, resp.GetErrorMessage(), "outside")
			require.NotContains(t, string(resp.GetPayload()), "secret.txt")
		}
	})

	// The regression that motivated the gate. An absent path passes the
	// dispatch-time payload check (an empty value resolves to the root), so
	// only the handler can decide what it means — and it used to mean
	// os.Getwd(), which during this test is the package source directory.
	t.Run("an absent path walks the root, not the daemon's working directory", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "tree-3",
			CommandType: "fs.get_tree",
			Payload:     mustPayload(t, map[string]any{"path": ""}),
			Policy:      policy(),
		})
		require.True(t, resp.GetSuccess(), "an absent path must mean the grant's root: %s", resp.GetErrorMessage())
		require.Contains(t, string(resp.GetPayload()), "inside.txt")
		require.NotContains(t, string(resp.GetPayload()), "cmd_fs.go",
			"the walk fell back to the daemon's working directory instead of the root")
	})

	t.Run("the root itself is walked", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "tree-4",
			CommandType: "fs.get_tree",
			Payload:     mustPayload(t, map[string]any{"path": root}),
			Policy:      policy(),
		})
		require.True(t, resp.GetSuccess(), "granted walk failed: %s", resp.GetErrorMessage())
		require.Contains(t, string(resp.GetPayload()), "inside.txt")
	})

	// The handler is the boundary, not the dispatch check. Calling it directly
	// with a policy context proves the confinement is in the handler itself,
	// so a payload shape the dispatch walker does not recognise still cannot
	// widen the walk.
	t.Run("the handler itself confines, not only the dispatch check", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires elevation on Windows")
		}
		victim := filepath.Join(root, "subdir")
		require.NoError(t, os.Symlink(outside, victim))

		ctx := daemonpolicy.NewContext(context.Background(), &daemonpolicy.Policy{
			GrantID:  "grant-tree",
			Tools:    map[string]bool{"fs.get_tree": true},
			PathRoot: root,
		})

		out, err := handleFSGetTree(ctx, mustPayload(t, map[string]any{"path": victim}))
		require.Error(t, err, "a symlink out of the root must be refused by the handler")
		require.NotContains(t, string(out), "secret.txt")

		// And with no policy at all, first-party behaviour is untouched.
		out, err = handleFSGetTree(context.Background(), mustPayload(t, map[string]any{"path": outside}))
		require.NoError(t, err)
		require.Contains(t, string(out), "secret.txt")
	})
}

// TestPolicyGate_ConfinesPreviewInfo covers fs.preview_info, which discarded
// its context and so had no handler-side gate at all.
//
// It was the least exposed of the ungated fs commands — dispatch-time
// checkPaths recognises its "path" key, and it has no empty-path fallback to
// the daemon's working directory — but dispatch is a fast reject, not the
// boundary. The handler stats the file and reads its first 8KB, returning the
// name, size, mtime and MIME type, so an escape here is a metadata oracle over
// the whole machine and a content sniff of every file on it.
func TestPolicyGate_ConfinesPreviewInfo(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("sensitive"), 0o600))
	inside := filepath.Join(root, "inside.txt")
	require.NoError(t, os.WriteFile(inside, []byte("ok"), 0o600))

	policy := func() *reliantv1.ConnectorPolicy {
		return &reliantv1.ConnectorPolicy{
			GrantId:      "grant-preview",
			AllowedTools: []string{"fs.preview_info"},
			PathRoot:     root,
		}
	}

	t.Run("an absolute path outside the root is refused", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "preview-1",
			CommandType: "fs.preview_info",
			Payload:     mustPayload(t, map[string]any{"path": secret}),
			Policy:      policy(),
		})
		require.False(t, resp.GetSuccess())
		require.Contains(t, resp.GetErrorMessage(), "outside")
		require.NotContains(t, string(resp.GetPayload()), "secret.txt")
	})

	t.Run("a relative traversal is refused", func(t *testing.T) {
		for _, escape := range []string{
			"..",
			"../..",
			"../" + filepath.Base(outside) + "/secret.txt",
		} {
			d := newPolicyGateClient(t)
			resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
				RequestId:   "preview-2",
				CommandType: "fs.preview_info",
				Payload:     mustPayload(t, map[string]any{"path": escape}),
				Policy:      policy(),
			})
			require.False(t, resp.GetSuccess(), "escape %q was permitted", escape)
			require.Contains(t, resp.GetErrorMessage(), "outside",
				"escape %q was rejected for the wrong reason", escape)
			require.NotContains(t, string(resp.GetPayload()), "secret.txt")
		}
	})

	t.Run("a file inside the root previews", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "preview-3",
			CommandType: "fs.preview_info",
			Payload:     mustPayload(t, map[string]any{"path": inside}),
			Policy:      policy(),
		})
		require.True(t, resp.GetSuccess(), "granted preview failed: %s", resp.GetErrorMessage())
		require.Contains(t, string(resp.GetPayload()), "inside.txt")
	})

	// The handler is the boundary, not the dispatch check.
	//
	// The dispatch walker infers intent from argument NAMES, and the path it
	// approved can be swapped for a symlink out of the root before the handler
	// opens it. Both are reproduced here by calling the handler directly with
	// a policy context — which is exactly the state the daemon is in the
	// instant after checkPaths returns.
	t.Run("the handler itself confines, not only the dispatch check", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires elevation on Windows")
		}

		ctx := daemonpolicy.NewContext(context.Background(), &daemonpolicy.Policy{
			GrantID:  "grant-preview",
			Tools:    map[string]bool{"fs.preview_info": true},
			PathRoot: root,
		})

		// A real directory inside the root, holding a harmless file. The
		// dispatch check would approve a preview of it.
		victim := filepath.Join(root, "subdir")
		require.NoError(t, os.Mkdir(victim, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(victim, "secret.txt"), []byte("harmless"), 0o600))
		target := filepath.Join(victim, "secret.txt")

		out, err := handleFSPreviewInfo(ctx, mustPayload(t, map[string]any{"path": target}))
		require.NoError(t, err, "an in-bounds preview must still work")
		require.Contains(t, string(out), "secret.txt")

		// Now swap the directory for a symlink out of the root — the move the
		// dispatch check cannot see, because it already ran.
		require.NoError(t, os.RemoveAll(victim))
		require.NoError(t, os.Symlink(outside, victim))

		out, err = handleFSPreviewInfo(ctx, mustPayload(t, map[string]any{"path": target}))
		require.Error(t, err, "a path swapped for an escaping symlink must be refused by the handler")
		require.Contains(t, err.Error(), "outside")
		require.NotContains(t, string(out), "sensitive")

		// A path the dispatch check never saw at all — the handler is asked
		// directly for a file outside the root.
		out, err = handleFSPreviewInfo(ctx, mustPayload(t, map[string]any{"path": secret}))
		require.Error(t, err, "the handler must refuse an out-of-root path on its own")
		require.NotContains(t, string(out), "sensitive")

		// A symlink whose final component points out of the root is refused
		// even when its parent is genuinely inside.
		link := filepath.Join(root, "link.txt")
		require.NoError(t, os.Symlink(secret, link))
		out, err = handleFSPreviewInfo(ctx, mustPayload(t, map[string]any{"path": link}))
		require.Error(t, err, "a symlink out of the root must be refused")
		require.NotContains(t, string(out), "sensitive")
	})

	// The regression guard. With no policy in the context the resolve is a
	// no-op, so first-party behaviour is byte-for-byte what it was.
	t.Run("an unconfined caller is unaffected", func(t *testing.T) {
		out, err := handleFSPreviewInfo(context.Background(), mustPayload(t, map[string]any{"path": secret}))
		require.NoError(t, err)
		require.Contains(t, string(out), "secret.txt")

		var resp struct {
			Path       string `json:"path"`
			Size       int64  `json:"size"`
			ViewerKind string `json:"viewer_kind"`
		}
		require.NoError(t, json.Unmarshal(out, &resp))
		require.Equal(t, secret, resp.Path, "an unconfined path must be returned unchanged")
		require.Equal(t, int64(len("sensitive")), resp.Size)
		require.NotEmpty(t, resp.ViewerKind)
	})
}

// TestPolicyGate_ConfinesRemainingFSHandlers covers the other cmd_fs.go
// handlers that discarded their context and therefore had no handler-side
// gate: fs.read_binary_file, fs.pdf_page_count, fs.read_pdf_pages, fs.stat,
// fs.mkdir, fs.delete and fs.copy.
//
// fs.preview_info was not the last one. Each is driven directly, past the
// dispatch check, because that is what the boundary claim actually means: a
// payload shape the dispatch walker does not recognise, or a symlink swapped
// in after it ran, must still be refused.
func TestPolicyGate_ConfinesRemainingFSHandlers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on Windows")
	}

	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("sensitive"), 0o600))

	// A symlink inside the root pointing out of it — the escape the dispatch
	// check cannot catch once it has already run.
	escapeLink := filepath.Join(root, "escape")
	require.NoError(t, os.Symlink(outside, escapeLink))
	viaLink := filepath.Join(escapeLink, "secret.txt")

	ctx := daemonpolicy.NewContext(context.Background(), &daemonpolicy.Policy{
		GrantID:  "grant-rest",
		Tools:    map[string]bool{"fs.read_binary_file": true},
		PathRoot: root,
	})

	cases := []struct {
		name    string
		handler func(context.Context, []byte) ([]byte, error)
		payload map[string]any
	}{
		{"fs.read_binary_file/absolute", handleFSReadBinaryFile, map[string]any{"path": secret}},
		{"fs.read_binary_file/symlink", handleFSReadBinaryFile, map[string]any{"path": viaLink}},
		{"fs.pdf_page_count/absolute", handleFSPDFPageCount, map[string]any{"path": secret}},
		{"fs.read_pdf_pages/absolute", handleFSReadPDFPages, map[string]any{"path": secret, "pages": "1"}},
		{"fs.stat/absolute", handleFSStat, map[string]any{"path": secret}},
		{"fs.stat/traversal", handleFSStat, map[string]any{"path": "../.."}},
		{"fs.stat/symlink", handleFSStat, map[string]any{"path": viaLink}},
		{"fs.mkdir/absolute", handleFSMkdir, map[string]any{"path": filepath.Join(outside, "planted")}},
		{"fs.mkdir/symlink", handleFSMkdir, map[string]any{"path": filepath.Join(escapeLink, "planted")}},
		{"fs.delete/absolute", handleFSDelete, map[string]any{"path": secret}},
		{"fs.delete/symlink", handleFSDelete, map[string]any{"path": viaLink}},
		{"fs.copy/source", handleFSCopy, map[string]any{
			"source": secret, "destination": filepath.Join(root, "copied.txt")}},
		{"fs.copy/destination", handleFSCopy, map[string]any{
			"source": secret, "destination": filepath.Join(outside, "exfil.txt")}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Re-created per case so one destructive handler cannot mask the
			// next: without the gate fs.delete really does remove this file,
			// and every later case would then "pass" only because its target
			// had already been destroyed.
			require.NoError(t, os.WriteFile(secret, []byte("sensitive"), 0o600))

			out, err := tc.handler(ctx, mustPayload(t, tc.payload))
			require.Error(t, err, "the handler must refuse a path outside the grant root")

			// The refusal must come from the policy, not from the handler
			// happening to dislike the file — otherwise fs.pdf_page_count
			// would "pass" merely because the bait is not a PDF.
			require.ErrorIs(t, err, daemonpolicy.ErrDenied,
				"refused for the wrong reason: %v", err)
			require.NotContains(t, string(out), "sensitive")

			// And the refusal happened before the filesystem was touched.
			require.FileExists(t, secret, "a refused handler still removed the file")
		})
	}

	// Nothing was created or removed outside the root.
	require.FileExists(t, secret, "a refused fs.delete still removed the file")
	_, err := os.Stat(filepath.Join(outside, "planted"))
	require.True(t, os.IsNotExist(err), "a refused fs.mkdir still created the directory")
	_, err = os.Stat(filepath.Join(outside, "exfil.txt"))
	require.True(t, os.IsNotExist(err), "a refused fs.copy still wrote outside the root")
	_, err = os.Stat(filepath.Join(root, "copied.txt"))
	require.True(t, os.IsNotExist(err), "a refused fs.copy still copied an out-of-root source in")

	// And with no policy at all, every one behaves exactly as before.
	t.Run("unconfined callers are unaffected", func(t *testing.T) {
		bg := context.Background()

		out, err := handleFSStat(bg, mustPayload(t, map[string]any{"path": secret}))
		require.NoError(t, err)
		require.Contains(t, string(out), `"exists":true`)

		out, err = handleFSReadBinaryFile(bg, mustPayload(t, map[string]any{"path": secret}))
		require.NoError(t, err)
		var bin struct {
			Data string `json:"data"`
		}
		require.NoError(t, json.Unmarshal(out, &bin))
		decoded, err := base64.StdEncoding.DecodeString(bin.Data)
		require.NoError(t, err)
		require.Equal(t, "sensitive", string(decoded))

		free := t.TempDir()
		require.NoError(t, func() error {
			_, err := handleFSMkdir(bg, mustPayload(t, map[string]any{"path": filepath.Join(free, "made")}))
			return err
		}())
		require.DirExists(t, filepath.Join(free, "made"))

		dst := filepath.Join(free, "copy.txt")
		_, err = handleFSCopy(bg, mustPayload(t, map[string]any{"source": secret, "destination": dst}))
		require.NoError(t, err)
		require.FileExists(t, dst)

		_, err = handleFSDelete(bg, mustPayload(t, map[string]any{"path": dst}))
		require.NoError(t, err)
		require.NoFileExists(t, dst)
	})
}
