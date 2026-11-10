// Copyright (c) 2025 Reliant Labs
package ghshim

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestMain lets the test binary stand in for the reliant binary the shim
// script execs.
func TestMain(m *testing.M) {
	Dispatch()
	os.Exit(m.Run())
}

const chat = "11111111-2222-4333-8444-555555555555"

const marker = "<!-- reliant-chat: " + chat + " -->"

func chatEnv(extra ...string) []string {
	return append([]string{"PATH=/usr/bin:/bin", ChatIDEnv + "=" + chat}, extra...)
}

// bodyOf is the body a plan hands gh: the --body-file it names (read) or --body.
func bodyOf(t *testing.T, p plan) string {
	t.Helper()
	for i, a := range p.args {
		if a == "--body-file" && i+1 < len(p.args) {
			b, err := os.ReadFile(p.args[i+1])
			if err != nil {
				t.Fatalf("read the rewritten body: %v", err)
			}
			return string(b)
		}
		if a == "--body" && i+1 < len(p.args) {
			return p.args[i+1]
		}
	}
	t.Fatalf("no body in %q", p.args)
	return ""
}

func TestPlanCreateStampsTheGivenBody(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "body.md"), []byte("From a file.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		args  []string
		stdin string
		want  string
	}{
		{"--body", []string{"pr", "create", "--title", "t", "--body", "Fixes #1."}, "", "Fixes #1.\n\n" + marker},
		{"--body=", []string{"pr", "create", "--title=t", "--body=Fixes #1.\n"}, "", "Fixes #1.\n\n" + marker},
		{"-b", []string{"pr", "create", "-t", "t", "-b", "Fixes #1."}, "", "Fixes #1.\n\n" + marker},
		{"-bVALUE", []string{"pr", "create", "-tt", "-bFixes #1."}, "", "Fixes #1.\n\n" + marker},
		{"empty body", []string{"pr", "create", "--fill", "--body", ""}, "", marker},
		{"--body-file", []string{"pr", "create", "--title", "t", "--body-file", "body.md", "--draft"}, "", "From a file.\n\n" + marker},
		{"-F", []string{"pr", "create", "-t", "t", "-F", filepath.Join(cwd, "body.md")}, "", "From a file.\n\n" + marker},
		{"--body-file -", []string{"pr", "create", "--title", "t", "--body-file", "-"}, "From stdin.", "From stdin.\n\n" + marker},
		{"pr new", []string{"pr", "new", "-R", "o/r", "--title", "t", "--body", "x"}, "", "x\n\n" + marker},
		{"last --body wins", []string{"pr", "create", "--body", "first", "--title", "t", "--body", "second"}, "", "second\n\n" + marker},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := planCreate(tc.args, chatEnv(), cwd, strings.NewReader(tc.stdin))
			defer p.cleanup()
			if p.fillAfter {
				t.Fatalf("a given body is stamped before gh runs, not after")
			}
			if got := bodyOf(t, p); got != tc.want {
				t.Fatalf("body = %q, want %q", got, tc.want)
			}
			if p.args[0] != tc.args[0] || p.args[1] != tc.args[1] {
				t.Fatalf("subcommand changed: %q", p.args)
			}
			for _, a := range p.args {
				if a == "--body" || a == "-b" || strings.HasPrefix(a, "--body=") || strings.HasPrefix(a, "-bF") || a == "-F" {
					t.Fatalf("the original body flag must be replaced: %q", p.args)
				}
			}
			for _, keep := range []string{"--draft", "-R"} {
				if slices.Contains(tc.args, keep) && !slices.Contains(p.args, keep) {
					t.Fatalf("%s dropped: %q", keep, p.args)
				}
			}
			p.cleanup()
			if p.tmp != "" {
				if _, err := os.Stat(p.tmp); !os.IsNotExist(err) {
					t.Fatalf("cleanup must remove %s", p.tmp)
				}
			}
		})
	}
}

func TestPlanCreateLeavesTheCommandAloneWhenItShould(t *testing.T) {
	cwd := t.TempDir()
	marked := "Fixes #1.\n\n<!-- reliant-chat: 99999999-2222-4333-8444-555555555555 -->"
	if err := os.WriteFile(filepath.Join(cwd, "marked.md"), []byte(marked), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		env  []string
	}{
		{"already marked", []string{"pr", "create", "--title", "t", "--body", marked}, chatEnv()},
		{"other marker wins", []string{"pr", "create", "--title", "t", "--body", "x <!-- reliant-chat: someone --> y"}, chatEnv()},
		{"file already marked", []string{"pr", "create", "--title", "t", "--body-file", "marked.md"}, chatEnv()},
		{"no chat", []string{"pr", "create", "--title", "t", "--body", "x"}, []string{"PATH=/bin"}},
		{"chat id not a uuid", []string{"pr", "create", "--title", "t", "--body", "x"}, []string{ChatIDEnv + "=$(rm -rf /)"}},
		{"turned off", []string{"pr", "create", "--title", "t", "--body", "x"}, chatEnv(OffEnv + "=off")},
		{"not pr create", []string{"pr", "edit", "1", "--body", "x"}, chatEnv()},
		{"unknown flag", []string{"pr", "create", "--title", "t", "--body", "x", "--new-flag"}, chatEnv()},
		{"positional", []string{"pr", "create", "--title", "t", "--body", "x", "extra"}, chatEnv()},
		{"both bodies", []string{"pr", "create", "--title", "t", "--body", "x", "--body-file", "marked.md"}, chatEnv()},
		{"missing body file", []string{"pr", "create", "--title", "t", "--body-file", "nope.md"}, chatEnv()},
		{"body in a cluster", []string{"pr", "create", "--title", "t", "-db", "x"}, chatEnv()},
		{"web", []string{"pr", "create", "--web"}, chatEnv()},
		{"editor", []string{"pr", "create", "-e", "--body", "x"}, chatEnv()},
		{"recover", []string{"pr", "create", "--recover", "f.json"}, chatEnv()},
		{"no body, no fill", []string{"pr", "create", "--title", "t", "--template", "t.md"}, chatEnv()},
		{"fill dry run", []string{"pr", "create", "--fill", "--dry-run"}, chatEnv()},
		{"value flag at the end", []string{"pr", "create", "--title"}, chatEnv()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := planCreate(tc.args, tc.env, cwd, strings.NewReader(""))
			defer p.cleanup()
			if p.fillAfter || p.tmp != "" || !slices.Equal(p.args, tc.args) {
				t.Fatalf("plan = %+v, want the arguments unchanged", p)
			}
		})
	}
}

func TestPlanCreateHandsBackStdinItRead(t *testing.T) {
	marked := "Body.\n" + marker
	p := planCreate([]string{"pr", "create", "-t", "t", "-F", "-"}, chatEnv(), t.TempDir(), strings.NewReader(marked))
	defer p.cleanup()
	if got := bodyOf(t, p); got != marked {
		t.Fatalf("a marked body read from stdin must reach gh as it was: %q", got)
	}
}

func TestPlanCreateFillMarksAfterCreation(t *testing.T) {
	for _, flag := range []string{"--fill", "-f", "--fill-first", "--fill-verbose", "-df"} {
		args := []string{"pr", "create", flag}
		p := planCreate(args, chatEnv(), t.TempDir(), strings.NewReader(""))
		if !p.fillAfter || p.chat != chat || !slices.Equal(p.args, args) {
			t.Fatalf("%s: plan = %+v, want gh run as given and the body marked after", flag, p)
		}
	}
	if p := planCreate([]string{"pr", "create", "--fill=false", "--title", "t"}, chatEnv(), t.TempDir(), strings.NewReader("")); p.fillAfter {
		t.Fatal("--fill=false is not a fill")
	}
}

func TestPlanCreateKeepsAnAttachedBodyBesideItsFile(t *testing.T) {
	cwd := t.TempDir()
	sub := filepath.Join(cwd, "docs")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.md"), []byte("![x](./x.png)"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := planCreate([]string{"pr", "create", "-t", "t", "--attach", "docs/x.png", "-F", "docs/b.md"}, chatEnv(), cwd, strings.NewReader(""))
	defer p.cleanup()
	if filepath.Dir(p.tmp) != sub {
		t.Fatalf("with --attach the rewritten body must sit beside its file (gh resolves references against it): %s", p.tmp)
	}
	p2 := planCreate([]string{"pr", "create", "-t", "t", "--attach", "x.png", "-b", "![x](./x.png)"}, chatEnv(), cwd, strings.NewReader(""))
	defer p2.cleanup()
	if filepath.Dir(p2.tmp) != cwd {
		t.Fatalf("with --attach and --body the rewritten body must sit in the working directory: %s", p2.tmp)
	}
}

func TestShimEnv(t *testing.T) {
	s := &Shim{dir: "/shim"}
	got := s.Env(chatEnv())
	if v, _ := lookupEnv(got, "PATH"); v != "/shim:/usr/bin:/bin" {
		t.Fatalf("PATH = %q", v)
	}
	if again := s.Env(got); len(again) != len(got) {
		t.Fatal("the shim must go on PATH once")
	}
	for name, env := range map[string][]string{
		"no chat": {"PATH=/bin"},
		"no PATH": {ChatIDEnv + "=" + chat},
		"inherit": nil,
	} {
		if got := s.Env(env); !slices.Equal(got, env) {
			t.Fatalf("%s: env changed: %q", name, got)
		}
	}
	var none *Shim
	if got := none.Env(chatEnv()); !slices.Equal(got, chatEnv()) {
		t.Fatal("a nil shim changes nothing")
	}
}

func TestInstallIsPerExecutableAndIdempotent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shim is a POSIX shell script")
	}
	root := t.TempDir()
	a, err := Install(root, "/opt/reliant/bin/reliant")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(a.Dir(), "gh"))
	if err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("the shim must be an executable gh: %v %v", fi, err)
	}
	a2, err := Install(root, "/opt/reliant/bin/reliant")
	if err != nil || a2.Dir() != a.Dir() {
		t.Fatalf("reinstall: %v %s", err, a2.Dir())
	}
	b, err := Install(root, "/other/reliant")
	if err != nil || b.Dir() == a.Dir() {
		t.Fatalf("another build gets its own directory: %v", err)
	}
	if _, err := Install(root, "relative"); err == nil {
		t.Fatal("a relative executable must be refused")
	}
}

// fakeGh writes a gh that logs its arguments and every --body-file's
// contents to $FAKE_GH_LOG, prints a pull request URL for `pr create`, the
// body for `pr view`, and exits $FAKE_GH_EXIT.
func fakeGh(t *testing.T) (dir, log string) {
	t.Helper()
	dir = t.TempDir()
	log = filepath.Join(t.TempDir(), "gh.log")
	script := `#!/bin/sh
printf 'ARGS:%s\n' "$*" >>"$FAKE_GH_LOG"
case ":$PATH:" in *"$FAKE_SHIM_DIR"*) echo "PATH still has the shim" >>"$FAKE_GH_LOG";; esac
prev=
for a in "$@"; do
  if [ "$prev" = "--body-file" ]; then printf 'BODY:' >>"$FAKE_GH_LOG"; cat "$a" >>"$FAKE_GH_LOG"; printf '\n' >>"$FAKE_GH_LOG"; fi
  prev=$a
done
case "$1 $2" in
"pr create") echo "https://github.com/o/r/pull/7" ;;
"pr view") echo "Commit body." ;;
esac
exit "${FAKE_GH_EXIT:-0}"
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, log
}

// runShim runs a shell command with the shim first on PATH, as a chat's
// shell does.
func runShim(t *testing.T, command string, extraEnv ...string) (string, int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the shim is a POSIX shell script")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Install(t.TempDir(), self)
	if err != nil {
		t.Fatal(err)
	}
	ghDir, log := fakeGh(t)
	env := s.Env([]string{
		"PATH=" + ghDir + ":/usr/bin:/bin",
		ChatIDEnv + "=" + chat,
		"FAKE_GH_LOG=" + log,
		"FAKE_SHIM_DIR=" + s.Dir(),
		"TMPDIR=" + t.TempDir(),
	})
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = append(env, extraEnv...)
	cmd.Dir = t.TempDir()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run: %v", err)
		}
		code = ee.ExitCode()
	}
	b, _ := os.ReadFile(log)
	return string(b) + "OUT:" + out.String(), code
}

func TestShimMarksAPullRequestOpenedFromTheShell(t *testing.T) {
	got, code := runShim(t, `gh pr create --title t --body 'Fixes #1.'`)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, got)
	}
	if !strings.Contains(got, "BODY:Fixes #1.\n\n"+marker) {
		t.Fatalf("the body gh got must carry the marker:\n%s", got)
	}
	if !strings.Contains(got, "OUT:https://github.com/o/r/pull/7") {
		t.Fatalf("gh's output must reach the caller:\n%s", got)
	}
	if strings.Contains(got, "PATH still has the shim") {
		t.Fatalf("the real gh must run without the shim on PATH:\n%s", got)
	}
}

func TestShimMarksAFilledBodyAfterCreation(t *testing.T) {
	got, code := runShim(t, `gh pr create --fill`)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, got)
	}
	for _, want := range []string{
		"ARGS:pr create --fill\n",
		"ARGS:pr view https://github.com/o/r/pull/7 --json body --jq .body\n",
		"BODY:Commit body.\n\n" + marker,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

func TestShimPassesEverythingElseStraightThrough(t *testing.T) {
	got, code := runShim(t, `gh pr view 12 --json body`, "FAKE_GH_EXIT=4")
	if code != 4 {
		t.Fatalf("gh's exit status must be the caller's: %d\n%s", code, got)
	}
	if !strings.Contains(got, "ARGS:pr view 12 --json body\n") || strings.Contains(got, "BODY:") {
		t.Fatalf("an unrelated gh command runs as given:\n%s", got)
	}
	if strings.Contains(got, "PATH still has the shim") {
		t.Fatalf("the real gh must run without the shim on PATH:\n%s", got)
	}
	// gh's own exit status, also through the stamping path.
	if _, code := runShim(t, `gh pr create --title t --body x`, "FAKE_GH_EXIT=1"); code != 1 {
		t.Fatalf("exit status through the rewrite: %d", code)
	}
}

func TestShimRunsGhWhenTheBinaryIsGone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shim is a POSIX shell script")
	}
	s, err := Install(t.TempDir(), "/nonexistent/reliant")
	if err != nil {
		t.Fatal(err)
	}
	ghDir, log := fakeGh(t)
	cmd := exec.Command("/bin/sh", "-c", `gh pr create --title t --body x`)
	cmd.Env = s.Env([]string{"PATH=" + ghDir + ":/usr/bin:/bin", ChatIDEnv + "=" + chat, "FAKE_GH_LOG=" + log, "FAKE_SHIM_DIR=" + s.Dir()})
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gh must still run: %v\n%s", err, out)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "ARGS:pr create --title t --body x\n") {
		t.Fatalf("gh runs unmarked when reliant is missing:\n%s", b)
	}
}
