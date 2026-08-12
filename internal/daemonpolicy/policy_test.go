// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newTestPolicy returns a policy that permits the common fs/exec commands
// under root, so each test can narrow just the dimension it exercises.
func newTestPolicy(root string) *Policy {
	return &Policy{
		GrantID:  "grant_test",
		Tools:    map[string]bool{"fs.read_file": true, "fs.write_file": true, "exec.run": true, "fs.glob": true},
		PathRoot: root,
		ExecMode: ExecAllowlist,
		ExecAllowlist: map[string]bool{
			"git": true,
			"go":  true,
		},
	}
}

func payload(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func TestNilPolicyAllowsEverything(t *testing.T) {
	var p *Policy
	// The first-party path must be entirely unaffected by this package, including
	// commands no grant would ever permit.
	if err := p.Check("exec.run", payload(t, map[string]string{"command": "rm -rf /"})); err != nil {
		t.Fatalf("nil policy must not restrict first-party callers, got %v", err)
	}
}

func TestToolAllowlist(t *testing.T) {
	p := newTestPolicy(t.TempDir())

	if err := p.Check("fs.read_file", payload(t, map[string]string{"path": "a.txt"})); err != nil {
		t.Fatalf("granted command was denied: %v", err)
	}

	err := p.Check("worktree.delete_branch", payload(t, map[string]string{}))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("ungranted command should be denied, got %v", err)
	}
}

func TestZeroValuePolicyDeniesAll(t *testing.T) {
	// A Policy built by a buggy caller must fail closed rather than open.
	p := &Policy{}
	if err := p.Check("fs.read_file", payload(t, map[string]string{"path": "/etc/passwd"})); !errors.Is(err, ErrDenied) {
		t.Fatalf("zero-value policy must deny, got %v", err)
	}
}

func TestExpiredGrantDenied(t *testing.T) {
	p := newTestPolicy(t.TempDir())
	p.ExpiresAt = time.Now().Add(-time.Minute)

	if err := p.Check("fs.read_file", payload(t, map[string]string{"path": "a.txt"})); !errors.Is(err, ErrDenied) {
		t.Fatalf("expired grant must be denied, got %v", err)
	}
}

func TestPathConfinement(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "src")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	p := newTestPolicy(root)

	t.Run("inside root allowed", func(t *testing.T) {
		if err := p.Check("fs.read_file", payload(t, map[string]string{"path": inside})); err != nil {
			t.Fatalf("path inside root denied: %v", err)
		}
	})

	t.Run("relative path resolved against root", func(t *testing.T) {
		if err := p.Check("fs.read_file", payload(t, map[string]string{"path": "src/main.go"})); err != nil {
			t.Fatalf("relative path inside root denied: %v", err)
		}
	})

	t.Run("absolute path outside root denied", func(t *testing.T) {
		if err := p.Check("fs.read_file", payload(t, map[string]string{"path": "/etc/passwd"})); !errors.Is(err, ErrDenied) {
			t.Fatal("path outside root must be denied")
		}
	})

	t.Run("traversal denied", func(t *testing.T) {
		if err := p.Check("fs.read_file", payload(t, map[string]string{"path": "../../../etc/passwd"})); !errors.Is(err, ErrDenied) {
			t.Fatal("traversal must be denied")
		}
	})

	t.Run("sibling prefix denied", func(t *testing.T) {
		// "/tmp/xyz-other" must not pass a root of "/tmp/xyz" by string prefix.
		sibling := root + "-other"
		if err := os.MkdirAll(sibling, 0o755); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(sibling)

		if err := p.Check("fs.read_file", payload(t, map[string]string{"path": sibling})); !errors.Is(err, ErrDenied) {
			t.Fatal("sibling directory sharing a name prefix must be denied")
		}
	})

	t.Run("nonexistent path under root allowed", func(t *testing.T) {
		// Writing a new file is the common case and must not be blocked just
		// because the target does not exist yet.
		newFile := filepath.Join(root, "does", "not", "exist", "yet.txt")
		if err := p.Check("fs.write_file", payload(t, map[string]string{"path": newFile})); err != nil {
			t.Fatalf("new file under root denied: %v", err)
		}
	})

	t.Run("nonexistent path outside root denied", func(t *testing.T) {
		if err := p.Check("fs.write_file", payload(t, map[string]string{"path": "/etc/brand/new.conf"})); !errors.Is(err, ErrDenied) {
			t.Fatal("new file outside root must be denied")
		}
	})
}

// TestSymlinkEscape is the case a lexical path check gets wrong: a symlink
// inside the root pointing out of it.
func TestSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on Windows")
	}

	root := t.TempDir()
	outside := t.TempDir()

	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("sensitive"), 0o600); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	p := newTestPolicy(root)

	t.Run("read through symlink denied", func(t *testing.T) {
		attempt := filepath.Join(link, "secret.txt")
		if err := p.Check("fs.read_file", payload(t, map[string]string{"path": attempt})); !errors.Is(err, ErrDenied) {
			t.Fatal("reading through an escaping symlink must be denied")
		}
	})

	t.Run("write to new file under symlinked dir denied", func(t *testing.T) {
		// The target does not exist, so confinement must judge it by the
		// resolved parent rather than skipping the check.
		attempt := filepath.Join(link, "planted.txt")
		if err := p.Check("fs.write_file", payload(t, map[string]string{"path": attempt})); !errors.Is(err, ErrDenied) {
			t.Fatal("writing through an escaping symlink must be denied")
		}
	})
}

func TestNestedAndListPaths(t *testing.T) {
	root := t.TempDir()
	p := newTestPolicy(root)

	t.Run("nested object path denied", func(t *testing.T) {
		body := map[string]any{"opts": map[string]any{"path": "/etc/passwd"}}
		if err := p.Check("fs.read_file", payload(t, body)); !errors.Is(err, ErrDenied) {
			t.Fatal("a path nested in a sub-object must still be confined")
		}
	})

	t.Run("list of paths denied when any escapes", func(t *testing.T) {
		body := map[string]any{"paths": []string{"src/a.go", "/etc/passwd"}}
		if err := p.Check("fs.glob", payload(t, body)); !errors.Is(err, ErrDenied) {
			t.Fatal("an escaping entry in a path list must be denied")
		}
	})

	// fs.glob and fs.search carry their search root as opts.base_dir rather
	// than a top-level path. A confinement check that only inspected the top
	// level would let a connector search the entire filesystem.
	t.Run("opts base_dir confined", func(t *testing.T) {
		body := map[string]any{
			"pattern": "**/*",
			"opts":    map[string]any{"base_dir": "/etc"},
		}
		if err := p.Check("fs.glob", payload(t, body)); !errors.Is(err, ErrDenied) {
			t.Fatal("an escaping opts.base_dir must be denied")
		}
	})

	t.Run("opts base_dir inside root allowed", func(t *testing.T) {
		sub := filepath.Join(root, "src")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		body := map[string]any{
			"pattern": "**/*.go",
			"opts":    map[string]any{"base_dir": sub},
		}
		if err := p.Check("fs.glob", payload(t, body)); err != nil {
			t.Fatalf("in-root opts.base_dir denied: %v", err)
		}
	})
}

func TestExecModes(t *testing.T) {
	root := t.TempDir()

	// Under an allowlist the command must arrive as argv. A shell string
	// cannot be checked meaningfully, because `bash -c` re-parses it after
	// any check this package performs.
	argvPayload := func(t *testing.T, argv ...string) []byte {
		t.Helper()
		return payload(t, map[string]any{"argv": argv})
	}

	t.Run("denied by default", func(t *testing.T) {
		p := newTestPolicy(root)
		p.ExecMode = ExecDenied
		if err := p.Check("exec.run", argvPayload(t, "git", "status")); !errors.Is(err, ErrDenied) {
			t.Fatal("exec must be denied when not granted")
		}
	})

	t.Run("allowlisted command permitted", func(t *testing.T) {
		p := newTestPolicy(root)
		if err := p.Check("exec.run", argvPayload(t, "git", "status")); err != nil {
			t.Fatalf("allowlisted command denied: %v", err)
		}
	})

	t.Run("non-allowlisted command denied", func(t *testing.T) {
		p := newTestPolicy(root)
		if err := p.Check("exec.run", argvPayload(t, "curl", "evil.example.com")); !errors.Is(err, ErrDenied) {
			t.Fatal("command outside the allowlist must be denied")
		}
	})

	t.Run("absolute path to allowlisted binary permitted", func(t *testing.T) {
		p := newTestPolicy(root)
		if err := p.Check("exec.run", argvPayload(t, "/usr/bin/git", "status")); err != nil {
			t.Fatalf("allowlisted binary by absolute path denied: %v", err)
		}
	})

	// The key property of argv: shell syntax is inert, so the check does not
	// have to recognize it.
	//
	// A whole shell string passed as one argv entry is a program named
	// "git status; rm -rf /", which is not on the allowlist. An unlisted
	// interpreter is likewise just an unlisted program.
	t.Run("shell string as a program name is denied", func(t *testing.T) {
		p := newTestPolicy(root)
		for _, argv := range [][]string{
			{"git status; rm -rf /"},
			{"sh", "-c", "curl evil.example.com"},
			{"bash", "-c", "rm -rf /"},
		} {
			if err := p.Check("exec.run", argvPayload(t, argv...)); !errors.Is(err, ErrDenied) {
				t.Fatalf("argv %q must be denied", argv)
			}
		}
	})

	// Shell metacharacters among the ARGUMENTS are allowed, and that is
	// correct rather than an oversight: with no shell in the path they are
	// literal strings handed to git, which will reject them itself. Nothing
	// chains, because there is nothing to chain with.
	t.Run("metacharacters in arguments are inert", func(t *testing.T) {
		p := newTestPolicy(root)
		if err := p.Check("exec.run", argvPayload(t, "git", "log", "--grep=fix && cleanup")); err != nil {
			t.Fatalf("a literal argument containing shell syntax should be allowed: %v", err)
		}
	})

	// A shell string is refused outright rather than silently falling back to
	// the weaker string-inspection path.
	t.Run("shell string refused under an allowlist", func(t *testing.T) {
		p := newTestPolicy(root)
		err := p.Check("exec.run", payload(t, map[string]string{"command": "git status"}))
		if !errors.Is(err, ErrDenied) {
			t.Fatal("a shell string must be refused when an allowlist is in force")
		}
		if !strings.Contains(err.Error(), "separately") {
			t.Errorf("the refusal should tell the caller to supply argv, got %q", err)
		}
	})

	t.Run("unrestricted permits a shell string", func(t *testing.T) {
		p := newTestPolicy(root)
		p.ExecMode = ExecUnrestricted
		if err := p.Check("exec.run", payload(t, map[string]string{"command": "anything | goes"})); err != nil {
			t.Fatalf("unrestricted exec denied: %v", err)
		}
	})

	t.Run("unknown mode fails closed", func(t *testing.T) {
		p := newTestPolicy(root)
		p.ExecMode = ExecMode("typo")
		if err := p.Check("exec.run", argvPayload(t, "git", "status")); !errors.Is(err, ErrDenied) {
			t.Fatal("an unrecognized exec mode must fail closed")
		}
	})
}

func TestGlobAndSearchPatternsAreConfined(t *testing.T) {
	root := t.TempDir()
	p := newTestPolicy(root)

	denied := []struct {
		name string
		body map[string]any
	}{
		{"absolute pattern", map[string]any{"pattern": "/etc/**"}},
		{"traversing pattern", map[string]any{"pattern": "../../**"}},
		{"absolute file_glob", map[string]any{"pattern": "x", "opts": map[string]any{"file_glob": "/etc/*"}}},
		{"traversing file_glob", map[string]any{"pattern": "x", "opts": map[string]any{"file_glob": "../*"}}},
		{"windows absolute", map[string]any{"pattern": `C:\Users\**`}},
		// Brace groups defeat plain segment-splitting: neither of these
		// contains a ".." segment by the / delimiter, but both expand into
		// one that walks upward.
		{"brace traversal", map[string]any{"pattern": "{..,.}/**/*"}},
		{"brace traversal reversed", map[string]any{"pattern": "{.,..}/**/*.env"}},
		{"brace nested", map[string]any{"pattern": "a/{..,x}/b"}},
	}

	for _, tc := range denied {
		t.Run(tc.name, func(t *testing.T) {
			if err := p.Check("fs.glob", payload(t, tc.body)); !errors.Is(err, ErrDenied) {
				t.Fatalf("pattern escaping the root must be denied, got %v", err)
			}
		})
	}

	allowed := []struct {
		name string
		body map[string]any
	}{
		{"relative glob", map[string]any{"pattern": "**/*.go"}},
		{"subdirectory glob", map[string]any{"pattern": "src/**/*.ts"}},
		{"dotfile glob", map[string]any{"pattern": ".github/**"}},
	}

	for _, tc := range allowed {
		t.Run(tc.name, func(t *testing.T) {
			if err := p.Check("fs.glob", payload(t, tc.body)); err != nil {
				t.Fatalf("ordinary relative pattern denied: %v", err)
			}
		})
	}
}

// TestEmptyPathIsNotAFreePass covers the handlers that treat an absent path as
// "the daemon's working directory" or "$HOME" — neither of which is inside the
// grant's root, since the daemon is never chdir'd into it.
func TestEmptyPathIsNotAFreePass(t *testing.T) {
	// A root the daemon's cwd is definitely not inside.
	root := filepath.Join(t.TempDir(), "confined")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	p := newTestPolicy(root)
	p.Tools["fs.list_dir"] = true
	p.Tools["worktree.git_status"] = true

	// An empty value must resolve to the root rather than be skipped. Since
	// the root is valid, these are allowed — but the check must have RUN,
	// which the following no-root case proves.
	if err := p.Check("fs.list_dir", payload(t, map[string]any{"path": ""})); err != nil {
		t.Fatalf("empty path should resolve to the root: %v", err)
	}

	// With no root granted, an empty path must be denied rather than silently
	// falling back to the daemon's cwd.
	noRoot := &Policy{
		Tools:    map[string]bool{"fs.list_dir": true, "worktree.git_status": true},
		PathRoot: "",
	}
	if err := noRoot.Check("fs.list_dir", payload(t, map[string]any{"path": ""})); !errors.Is(err, ErrDenied) {
		t.Fatal("an empty path under a policy with no root must be denied")
	}
	if err := noRoot.Check("worktree.git_status", payload(t, map[string]any{"worktree_path": ""})); !errors.Is(err, ErrDenied) {
		t.Fatal("git_status with no path must not fall back to the daemon's working directory")
	}
}

// TestEmptyPayloadDoesNotBypassRootCheck: several commands accept an empty
// payload and act on the daemon's working directory.
func TestEmptyPayloadDoesNotBypassRootCheck(t *testing.T) {
	noRoot := &Policy{
		Tools:    map[string]bool{"fs.get_tree": true},
		PathRoot: "",
	}
	if err := noRoot.Check("fs.get_tree", nil); !errors.Is(err, ErrDenied) {
		t.Fatal("an empty payload must not bypass the missing-root check")
	}
	if err := noRoot.Check("fs.get_tree", []byte("{}")); !errors.Is(err, ErrDenied) {
		t.Fatal("an empty object must not bypass the missing-root check")
	}
}

// TestUnexpectedPathTypesDenied: a path key holding something other than a
// string is a shape confinement cannot check, so it must not pass.
func TestUnexpectedPathTypesDenied(t *testing.T) {
	p := newTestPolicy(t.TempDir())

	for _, body := range []map[string]any{
		{"path": 123},
		{"path": map[string]any{"nested": "/etc/passwd"}},
		{"path": []any{[]any{"../../etc/passwd"}}},
		{"path": true},
	} {
		if err := p.Check("fs.read_file", payload(t, body)); !errors.Is(err, ErrDenied) {
			t.Fatalf("a non-string path value must be denied, got %v for %v", err, body)
		}
	}

	// An explicit null is the one benign case: it carries no path at all, and
	// the handler will reject or default it.
	if err := p.Check("fs.read_file", payload(t, map[string]any{"path": nil})); err != nil {
		t.Fatalf("a null path should not itself be a denial: %v", err)
	}
}

// TestExecEnvironmentDenied covers the bypass that once made the allowlist
// meaningless: PATH=/planted git status ran a planted binary that a connector
// with write access could place inside its own root.
//
// The check is an ALLOWLIST of inert variables, not a denylist of dangerous
// ones — git alone reads arbitrary config from GIT_CONFIG_COUNT/KEY/VALUE,
// which turns an allowlisted `git` into arbitrary code execution with no flag
// to inspect, and enumerating every such variable across every tool is not
// something anyone can do correctly.
func TestExecEnvironmentDenied(t *testing.T) {
	p := newTestPolicy(t.TempDir())

	for _, key := range []string{
		"PATH", "LD_PRELOAD", "LD_AUDIT", "DYLD_INSERT_LIBRARIES",
		"GIT_SSH_COMMAND", "GIT_EXTERNAL_DIFF", "GIT_PAGER", "GIT_ASKPASS",
		// The git-config family: arbitrary execution with no flag involved.
		"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
		"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM",
		// Relocate git's view of the filesystem entirely.
		"GIT_DIR", "GIT_WORK_TREE", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		// Language runtimes and the shell.
		"NODE_OPTIONS", "PYTHONPATH", "PYTHONSTARTUP", "RUBYOPT", "PERL5OPT",
		"JAVA_TOOL_OPTIONS", "BASH_ENV", "BASH_FUNC_x%%",
		// Anything unrecognized, which is the point of an allowlist.
		"SOME_FUTURE_VARIABLE",
	} {
		body := map[string]any{
			"argv": []string{"git", "status"},
			"env":  map[string]string{key: "/tmp/evil"},
		}
		if err := p.Check("exec.run", payload(t, body)); !errors.Is(err, ErrDenied) {
			t.Fatalf("setting %s must be denied", key)
		}
	}

	// Inert variables still work, so the check is not simply refusing env.
	for _, key := range []string{"CI", "TERM", "GIT_TERMINAL_PROMPT", "GIT_AUTHOR_NAME"} {
		body := map[string]any{
			"argv": []string{"git", "status"},
			"env":  map[string]string{key: "value"},
		}
		if err := p.Check("exec.run", payload(t, body)); err != nil {
			t.Fatalf("%s should be allowed: %v", key, err)
		}
	}

	// An inline prefix is now just a program name that is not on the allowlist.
	if err := p.Check("exec.run", payload(t, map[string]any{
		"argv": []string{"PATH=/tmp/evil", "git", "status"},
	})); !errors.Is(err, ErrDenied) {
		t.Fatal("an argv entry that looks like an env assignment must not be treated as one")
	}
}

// TestExecConfigFlagDenied covers arbitrary execution via a config flag, which
// contains no shell metacharacter and would otherwise pass.
func TestExecConfigFlagDenied(t *testing.T) {
	p := newTestPolicy(t.TempDir())

	for _, argv := range [][]string{
		{"git", "-c", "core.pager=sh", "log"},
		{"git", "--config", "core.pager=sh", "log"},
		{"git", "--config=core.pager=sh", "log"},
		{"git", "--upload-pack=/tmp/evil", "fetch"},
	} {
		if err := p.Check("exec.run", payload(t, map[string]any{"argv": argv})); !errors.Is(err, ErrDenied) {
			t.Fatalf("config-flag command %q must be denied", argv)
		}
	}

	// Ordinary usage still works.
	if err := p.Check("exec.run", payload(t, map[string]any{"argv": []string{"git", "status", "--short"}})); err != nil {
		t.Fatalf("ordinary allowlisted command denied: %v", err)
	}
}

func TestNullByteInPathDenied(t *testing.T) {
	p := newTestPolicy(t.TempDir())
	body := map[string]string{"path": "safe.txt\x00/../../etc/passwd"}
	if err := p.Check("fs.read_file", payload(t, body)); !errors.Is(err, ErrDenied) {
		t.Fatal("a path containing a NUL byte must be denied")
	}
}

func TestNoPathRootDeniesFilesystemAccess(t *testing.T) {
	p := &Policy{
		Tools:    map[string]bool{"fs.read_file": true},
		PathRoot: "",
	}
	if err := p.Check("fs.read_file", payload(t, map[string]string{"path": "anything.txt"})); !errors.Is(err, ErrDenied) {
		t.Fatal("an empty path root must deny access rather than allow everything")
	}
}

func TestMalformedPayloadDenied(t *testing.T) {
	p := newTestPolicy(t.TempDir())
	if err := p.Check("fs.read_file", []byte("{not json")); !errors.Is(err, ErrDenied) {
		t.Fatal("an unparseable payload must be denied, since it cannot be confined")
	}
}

// TestFilesystemRootGrantsWholeDisk pins the one root that needs a special
// case: "/" already ends in a separator, so the naive prefix check built "//"
// and denied every path. The consent UI offers "entire machine" as a
// deliberate choice, and a grant that silently denies everything is worse than
// one that refuses to be created.
func TestFilesystemRootGrantsWholeDisk(t *testing.T) {
	p := &Policy{
		Tools:    map[string]bool{"fs.read_file": true},
		PathRoot: string(filepath.Separator),
	}

	for _, path := range []string{"/etc/hosts", "/Users/someone/code/main.go"} {
		payload := []byte(`{"path":"` + path + `"}`)
		if err := p.Check("fs.read_file", payload); err != nil {
			t.Fatalf("root grant must permit %s: %v", path, err)
		}
	}
}

// A narrow root must still exclude a sibling whose name merely shares its
// prefix — the reason within() checks the separator at all.
func TestNarrowRootStillExcludesSiblings(t *testing.T) {
	root := t.TempDir()
	p := &Policy{
		Tools:    map[string]bool{"fs.read_file": true},
		PathRoot: filepath.Join(root, "project"),
	}

	payload := []byte(`{"path":"` + filepath.Join(root, "project-other", "secret") + `"}`)
	if err := p.Check("fs.read_file", payload); !errors.Is(err, ErrDenied) {
		t.Fatal("a sibling directory sharing the root's prefix must not be reachable")
	}
}

func TestContextRoundTrip(t *testing.T) {
	if got := FromContext(context.Background()); got != nil {
		t.Fatal("a bare context must yield no policy, meaning unrestricted")
	}

	p := newTestPolicy(t.TempDir())
	ctx := NewContext(context.Background(), p)
	if got := FromContext(ctx); got != p {
		t.Fatal("policy did not round-trip through the context")
	}

	// Attaching nil must not wrap the context in a value carrying a typed nil.
	if got := FromContext(NewContext(context.Background(), nil)); got != nil {
		t.Fatal("attaching a nil policy must leave the context unrestricted")
	}
}
