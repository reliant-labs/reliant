// Copyright (c) 2026 Reliant Labs
package vendorcli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// secret is what every fake vendor CLI prints beside its verdict: account
// data a status check must never let out of the machine's shell.
const secret = "SECRET-account-data-9f3a"

// sysTools are the system programs the scripts need; the tests put only
// these (symlinked) and the fakes on PATH, so a real vendor CLI installed on
// the test machine is never found.
var sysTools = []string{"grep", "timeout", "awk", "mkdir", "mktemp", "ln", "rm", "mv", "uname", "sha256sum", "cat", "tar", "chmod"}

type machine struct {
	t    *testing.T
	home string
	fake string // first on PATH
	sys  string
	log  string // fakes append "<name> <args>" here
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixtures require POSIX shell scripts and symlinks")
	}
	root := t.TempDir()
	m := &machine{t: t, home: filepath.Join(root, "home"), fake: filepath.Join(root, "fake"), sys: filepath.Join(root, "sys"), log: filepath.Join(root, "calls.log")}
	for _, d := range []string{m.home, m.fake, m.sys} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	for _, tool := range sysTools {
		if p, err := exec.LookPath(tool); err == nil {
			require.NoError(t, os.Symlink(p, filepath.Join(m.sys, tool)))
		}
	}
	return m
}

// fakeIn writes an executable named name into dir whose body is sh; it logs
// its arguments (and NO_OPEN_BROWSER / NO_BROWSER when set) first.
func (m *machine) fakeIn(dir, name, body string) string {
	m.t.Helper()
	p := filepath.Join(dir, name)
	script := "#!/bin/sh\nprintf '%s %s|%s|%s\\n' '" + name + "' \"$*\" \"${NO_OPEN_BROWSER:-}\" \"${NO_BROWSER:-}\" >>'" + m.log + "'\n" + body + "\n"
	require.NoError(m.t, os.WriteFile(p, []byte(script), 0o755))
	return p
}

func (m *machine) fake1(name, body string) { m.fakeIn(m.fake, name, body) }

func (m *machine) homeFile(rel, content string) {
	m.t.Helper()
	p := filepath.Join(m.home, rel)
	require.NoError(m.t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(m.t, os.WriteFile(p, []byte(content), 0o600))
}

func (m *machine) calls() string {
	b, _ := os.ReadFile(m.log)
	return string(b)
}

// run runs script with /bin/sh, as a run step's probe or `machine login` would.
func (m *machine) run(script string) (string, int) {
	m.t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = []string{"HOME=" + m.home, "PATH=" + m.fake + ":" + m.sys, "TMPDIR=" + m.t.TempDir()}
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else {
		require.NoError(m.t, err)
	}
	return string(out), code
}

func (m *machine) status(ids ...string) (map[string]Detail, string) {
	m.t.Helper()
	// set -u: the script is appended to requirements' probe, which runs under it.
	out, code := m.run("set -u\n" + StatusScript(ids))
	require.Equal(m.t, 0, code, out)
	assert.NotContains(m.t, out, secret, "vendor output must never leave the status script")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, l := range lines {
		assert.True(m.t, strings.HasPrefix(l, "RELIANT_CLI\t"), "only verdict lines: %q", l)
	}
	return ParseStatusDetail(out), out
}

func TestTable(t *testing.T) {
	assert.Equal(t, []string{"claude", "codex", "gemini", "cursor", "copilot"}, IDs())
	all := All()
	require.Len(t, all, 5)
	assert.Equal(t, Vendor{ID: "cursor", Name: "Cursor Agent", Binary: "cursor-agent"}, all[3])
	v, ok := Lookup("copilot")
	assert.True(t, ok)
	assert.Equal(t, "GitHub Copilot CLI", v.Name)
	_, ok = Lookup("claud")
	assert.False(t, ok)
	for _, e := range vendors {
		if e.ID != "cursor" {
			assert.NotEmpty(t, e.npmVersion, "%s is installed at a pinned version", e.ID)
		}
	}
	for _, tb := range append(append([]tarball{}, cursorTarballs...), ghTarballs...) {
		assert.Len(t, tb.sha256, 64, tb.url)
		assert.True(t, strings.HasPrefix(tb.url, "https://"), tb.url)
	}
}

func TestStatusClaude(t *testing.T) {
	cases := map[string]struct {
		body string
		want State
	}{
		"signed in":  {`printf '{"loggedIn": true, "email": "` + secret + `"}\n'`, StateSignedIn},
		"signed out": {`printf '{\n  "loggedIn": false,\n  "authMethod": "none"\n}\n'; exit 1`, StateSignedOut},
		"garbage":    {`echo "` + secret + `"; exit 3`, StateUnknown},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := newMachine(t)
			m.fake1("claude", tc.body)
			got, _ := m.status("claude")
			assert.Equal(t, Detail{State: tc.want}, got["claude"])
			assert.Contains(t, m.calls(), "claude auth status --json|")
		})
	}
}

func TestStatusCodex(t *testing.T) {
	cases := map[string]struct {
		body string
		want State
	}{
		"signed in":    {`echo "Logged in using ChatGPT ` + secret + `"`, StateSignedIn},
		"signed out":   {`echo "Not logged in" >&2; exit 1`, StateSignedOut},
		"config error": {`echo "error loading config ` + secret + `" >&2; exit 1`, StateUnknown},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := newMachine(t)
			m.fake1("codex", tc.body)
			got, _ := m.status("codex")
			assert.Equal(t, tc.want, got["codex"].State)
			assert.Contains(t, m.calls(), "codex login status|")
		})
	}
}

func TestStatusGeminiChecksTheFileWithoutReadingIt(t *testing.T) {
	m := newMachine(t)
	m.fake1("gemini", "exit 0")
	got, _ := m.status("gemini")
	assert.Equal(t, StateSignedOut, got["gemini"].State, "no oauth_creds.json")

	m.homeFile(".gemini/oauth_creds.json", "")
	got, _ = m.status("gemini")
	assert.Equal(t, StateSignedOut, got["gemini"].State, "an empty file is not a sign-in")

	m.homeFile(".gemini/oauth_creds.json", `{"refresh_token":"`+secret+`"}`)
	got, _ = m.status("gemini")
	assert.Equal(t, StateSignedIn, got["gemini"].State)
	assert.Empty(t, m.calls(), "gemini has no status command; the check never runs it")
}

func TestStatusCursor(t *testing.T) {
	cases := map[string]State{
		`{"status": "authenticated", "isAuthenticated": true, "userInfo": {"email": "` + secret + `"}}`: StateSignedIn,
		`{"status": "partially-authenticated", "isAuthenticated": false}`:                               StateExpired,
		`{"status": "unauthenticated", "isAuthenticated": false, "message": "Not logged in"}`:           StateSignedOut,
		`error: unknown option '--format'`:                                                              StateUnknown,
	}
	for out, want := range cases {
		t.Run(string(want), func(t *testing.T) {
			m := newMachine(t)
			m.fake1("cursor-agent", "printf '%s\\n' '"+out+"'")
			got, _ := m.status("cursor")
			assert.Equal(t, want, got["cursor"].State)
			assert.Contains(t, m.calls(), "cursor-agent status --format json|")
		})
	}
}

func TestStatusCopilot(t *testing.T) {
	cases := map[string]struct {
		gh   string // "" = no gh at all
		want State
	}{
		"signed in":     {`echo "Logged in to github.com account ` + secret + `" >&2`, StateSignedIn},
		"token invalid": {`printf 'github.com\n  X Failed to log in to github.com account ` + secret + `\n  - The token in /h/hosts.yml is invalid.\n' >&2; exit 1`, StateExpired},
		"signed out":    {`echo "You are not logged into any GitHub hosts. To log in, run: gh auth login" >&2; exit 1`, StateSignedOut},
		"network":       {`echo "error connecting to api.github.com" >&2; exit 1`, StateUnknown},
		"no gh":         {"", StateSignedOut},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := newMachine(t)
			m.fake1("copilot", "exit 0")
			if tc.gh != "" {
				m.fake1("gh", tc.gh)
			}
			got, _ := m.status("copilot")
			assert.Equal(t, tc.want, got["copilot"].State)
			if tc.gh != "" {
				assert.Contains(t, m.calls(), "gh auth status --hostname github.com|")
			}
			assert.NotContains(t, m.calls(), "copilot ", "the copilot binary is only looked for, never run")
		})
	}
}

func TestStatusMissingAndOffPath(t *testing.T) {
	m := newMachine(t)
	lbin := filepath.Join(m.home, ".local", "bin")
	require.NoError(t, os.MkdirAll(lbin, 0o755))
	m.fakeIn(lbin, "claude", `printf '{"loggedIn": true}\n'`)
	m.fake1("codex", "exit 0")
	// copilot on PATH, but its gh only in ~/.local/bin: run steps cannot reach it.
	m.fake1("copilot", "exit 0")
	m.fakeIn(lbin, "gh", "exit 0")

	got, out := m.status(IDs()...)
	assert.Equal(t, Detail{State: StateSignedIn, OffPath: true}, got["claude"])
	assert.Equal(t, Detail{State: StateSignedIn}, got["codex"])
	assert.Equal(t, Detail{State: StateMissing}, got["gemini"])
	assert.Equal(t, Detail{State: StateMissing}, got["cursor"])
	assert.Equal(t, Detail{State: StateSignedIn, OffPath: true}, got["copilot"])
	assert.Len(t, strings.Split(strings.TrimSpace(out), "\n"), 5, "exactly one line per CLI")
}

func TestStatusScriptSkipsUnknownAndDuplicateIDs(t *testing.T) {
	m := newMachine(t)
	m.fake1("codex", "exit 0")
	got, out := m.status("codex", "codex", "claud'; rm -rf /; '")
	assert.Equal(t, 1, strings.Count(out, "RELIANT_CLI"), out)
	assert.Equal(t, StateSignedIn, got["codex"].State)
	assert.NotContains(t, StatusScript([]string{"claud'; rm -rf /; '"}), "rm -rf")
}

func TestParseStatus(t *testing.T) {
	out := strings.Join([]string{
		"some unrelated line",
		"RELIANT_CLI\tclaude\tsigned_in",
		"RELIANT_CLI\tcodex\texpired\tlocal_bin",
		"RELIANT_CLI\tgemini\tsomething_new",
		"RELIANT_CLI\tnotavendor\tsigned_in",
		"RELIANT_CLI\tcursor",
		"TOOL_MISSING\tgit",
	}, "\r\n")
	got := ParseStatus(out)
	assert.Equal(t, map[string]State{
		"claude": StateSignedIn, "codex": StateExpired, "gemini": StateUnknown,
		"cursor": StateUnknown, "copilot": StateUnknown,
	}, got)
	assert.True(t, ParseStatusDetail(out)["codex"].OffPath)
	assert.Len(t, ParseStatus(""), 5, "every vendor is in the result")
}

func TestLoginScriptUnknownVendor(t *testing.T) {
	_, err := LoginScript("claud")
	require.Error(t, err)
	assert.Equal(t, `"claud" is not a vendor CLI (claude, codex, gemini, cursor, copilot)`, err.Error())
	var uv UnknownVendorError
	assert.True(t, errors.As(err, &uv))
}

func TestLoginExecsTheVendorsOwnLogin(t *testing.T) {
	cases := map[string]struct {
		bin, want string
	}{
		"claude": {"claude", `claude auth login --claudeai||`},
		"codex":  {"codex", `codex login --device-auth -c cli_auth_credentials_store="file"||`},
		"cursor": {"cursor-agent", `cursor-agent login|1|`},
		"gemini": {"gemini", `gemini |`},
	}
	for id, tc := range cases {
		t.Run(id, func(t *testing.T) {
			m := newMachine(t)
			body := "exit 7"
			if id == "gemini" {
				// The session's system settings select the Google sign-in.
				body = `grep -q oauth-personal "$GEMINI_CLI_SYSTEM_SETTINGS_PATH" && exit 7; exit 1`
			}
			m.fake1(tc.bin, body)
			script, err := LoginScript(id)
			require.NoError(t, err)
			out, code := m.run(script)
			assert.Equal(t, 7, code, "the vendor login's exit code is the script's: %s", out)
			assert.Contains(t, m.calls(), tc.want)
			if id == "gemini" {
				assert.Contains(t, m.calls(), "|true\n", "NO_BROWSER")
			}
		})
	}
}

func TestLoginCopilotSignsInThroughGH(t *testing.T) {
	m := newMachine(t)
	m.fake1("copilot", "exit 0")
	m.fake1("gh", "exit 0")
	script, err := LoginScript("copilot")
	require.NoError(t, err)
	out, code := m.run(script)
	require.Equal(t, 0, code, out)
	assert.Equal(t, "gh auth login --hostname github.com --git-protocol https --web||\n", m.calls())
}

func TestLoginInstallsAMissingCLIWithNPM(t *testing.T) {
	m := newMachine(t)
	// A fake npm: `npm install ... --prefix DIR PKG` puts a fake claude in DIR/node_modules/.bin.
	m.fake1("npm", `prefix=''
while [ $# -gt 0 ]; do [ "$1" = --prefix ] && prefix=$2; shift; done
mkdir -p "$prefix/node_modules/.bin"
cat >"$prefix/node_modules/.bin/claude" <<'EOF'
#!/bin/sh
exit 5
EOF
chmod +x "$prefix/node_modules/.bin/claude"`)
	script, err := LoginScript("claude")
	require.NoError(t, err)
	out, code := m.run(script)
	assert.Equal(t, 5, code, "the installed claude's login ran: %s", out)
	assert.Contains(t, m.calls(), "npm install --no-audit --no-fund --loglevel=error --prefix "+filepath.Join(m.home, ".local/share/reliant-cli/claude")+" @anthropic-ai/claude-code@"+ClaudeCodeVersion)
	link, err := os.Readlink(filepath.Join(m.home, ".local/bin/claude"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(m.home, ".local/share/reliant-cli/claude/node_modules/.bin/claude"), link)
	assert.Contains(t, out, "is not on this PATH", "the person is told where it went")
}

func TestLoginWithoutNPMSaysSo(t *testing.T) {
	m := newMachine(t)
	script, err := LoginScript("codex")
	require.NoError(t, err)
	out, code := m.run(script)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "npm (Node.js) is not installed on this machine")
}

func TestLoginRefusesADownloadThatIsNotThePinnedOne(t *testing.T) {
	m := newMachine(t)
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum is not on this machine")
	}
	m.fake1("curl", `while [ $# -gt 1 ]; do [ "$1" = -o ] && out=$2; shift; done; echo tampered >"$out"`)
	script, err := LoginScript("cursor")
	require.NoError(t, err)
	out, code := m.run(script)
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "not the pinned")
	assert.Contains(t, m.calls(), "https://downloads.cursor.com/lab/"+CursorVersion+"/")
	_, err = os.Lstat(filepath.Join(m.home, ".local/bin/cursor-agent"))
	assert.True(t, os.IsNotExist(err), "nothing is linked")
}
