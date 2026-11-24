// Copyright (c) 2026 Reliant Labs

// Package vendorcli is the one table of the third-party agent CLIs a person
// signs in on a machine of theirs (Claude Code, Codex, Gemini CLI, Cursor
// Agent, GitHub Copilot CLI), and the shell that checks and starts those
// sign-ins on that machine.
//
// Reliant never reads, stores, copies, logs or relays a vendor's token. Every
// script here runs the vendor's own, unmodified CLI on the machine where its
// files live: a status check keeps the vendor's output inside the machine's
// shell and prints only a fixed verdict line, and a login execs the vendor's
// own login flow, which writes its own files. That is the whole contract:
// change a recipe only in a way that keeps it.
//
// The scripts are POSIX sh (no bashisms): run steps go through bash, and
// `reliant machine login` runs them with sh.
package vendorcli

import (
	"fmt"
	"strings"
)

// Vendor is one third-party agent CLI.
type Vendor struct {
	ID     string // "claude" | "codex" | "gemini" | "cursor" | "copilot"
	Name   string // "Claude Code", "Codex", "Gemini CLI", "Cursor Agent", "GitHub Copilot CLI"
	Binary string // "claude", "codex", "gemini", "cursor-agent", "copilot"
}

// State is what a status check found for one CLI on a machine.
type State string

const (
	// StateSignedIn: the vendor's own status says the CLI is signed in.
	StateSignedIn State = "signed_in"
	// StateSignedOut: the CLI is installed and not signed in.
	StateSignedOut State = "signed_out"
	// StateExpired: the vendor's status says the sign-in no longer works.
	// Only vendors whose status distinguishes it report it; no check ever
	// makes a model call to find out.
	StateExpired State = "expired"
	// StateMissing: the CLI is neither on PATH nor in $HOME/.local/bin.
	StateMissing State = "missing"
	// StateUnknown: the check could not tell (it timed out, the vendor's
	// output was unrecognised, or the machine never answered for this CLI).
	StateUnknown State = "unknown"
)

// Pinned installs. `reliant machine login` installs a missing CLI at exactly
// these versions, under $HOME (a cloud machine keeps $HOME across stop, start
// and resize; /usr/bin, /opt and /tmp it does not).
const (
	ClaudeCodeVersion = "2.1.330" // npm @anthropic-ai/claude-code
	CodexVersion      = "0.171.0" // npm @openai/codex
	GeminiCLIVersion  = "0.70.0"  // npm @google/gemini-cli
	CopilotVersion    = "1.0.114" // npm @github/copilot
	CursorVersion     = "2026.11.22-e9042b2"
	GHVersion         = "2.105.0" // github.com/cli/cli release; Copilot CLI signs in through gh
)

// tarball is a pinned download for one platform, checked against its sha256.
type tarball struct {
	os, arch string // uname-derived: linux|darwin, x64|arm64
	url      string
	sha256   string
	zip      bool // a .zip (gh on macOS), else a .tar.gz
}

// entry is one vendor's recipe: how to tell whether it is signed in, how to
// install it, how to sign it in.
type entry struct {
	Vendor
	npmPackage string // installed with npm at npmVersion; empty for cursor
	npmVersion string
	// status is sh that sets rcli_s to a State, given rcli_bin (the CLI's
	// path) and rcli_t (the timeout prefix). Vendor output never leaves it.
	status string
	// login is sh run after the CLI (rcli_bin) is installed; it ends by
	// running the vendor's own login.
	login string
}

const statusClaude = `rcli_o=$($rcli_t "$rcli_bin" auth status --json </dev/null 2>/dev/null)
if printf '%s' "$rcli_o" | grep -q '"loggedIn": *true'; then rcli_s=signed_in
elif printf '%s' "$rcli_o" | grep -q '"loggedIn": *false'; then rcli_s=signed_out
fi
`

const statusCodex = `rcli_o=$($rcli_t "$rcli_bin" login status </dev/null 2>&1); rcli_rc=$?
if [ "$rcli_rc" -eq 0 ]; then rcli_s=signed_in
elif printf '%s' "$rcli_o" | grep -qi 'not logged in'; then rcli_s=signed_out
fi
`

// Gemini CLI has no status command. Its Google sign-in is
// ~/.gemini/oauth_creds.json (the file store, its default); the check asks
// only that the file is there and not empty, and never reads it.
const statusGemini = `if [ -s "$HOME/.gemini/oauth_creds.json" ]; then rcli_s=signed_in; else rcli_s=signed_out; fi
`

// cursor-agent status exits 0 signed in or not; its JSON status names the
// state. "partially-authenticated" is an access token with no refresh token:
// a sign-in that stops working when the token does.
const statusCursor = `rcli_o=$($rcli_t "$rcli_bin" status --format json </dev/null 2>/dev/null)
if printf '%s' "$rcli_o" | grep -q '"status": *"authenticated"'; then rcli_s=signed_in
elif printf '%s' "$rcli_o" | grep -q '"status": *"partially-authenticated"'; then rcli_s=expired
elif printf '%s' "$rcli_o" | grep -q '"status": *"unauthenticated"'; then rcli_s=signed_out
fi
`

// Copilot CLI uses gh's sign-in: present means the copilot binary and a gh
// that `gh auth status` reports signed in to github.com. gh tests the token,
// and says "is invalid" when it no longer works. With no gh at all there is
// nothing signed in: `reliant machine login copilot` installs it.
const statusCopilot = `if rcli_find gh; then
  rcli_o=$($rcli_t "$rcli_bin" auth status --hostname github.com </dev/null 2>&1); rcli_rc=$?
  if [ "$rcli_rc" -eq 0 ]; then rcli_s=signed_in
  elif printf '%s' "$rcli_o" | grep -qiE 'is invalid|has expired'; then rcli_s=expired
  elif printf '%s' "$rcli_o" | grep -qi 'not logged in'; then rcli_s=signed_out
  fi
else rcli_s=signed_out
fi
`

const loginClaude = `printf '%s\n' "Sign in with your Claude account. On a machine with no browser, open the URL it prints and paste the code back."
exec "$rcli_bin" auth login --claudeai
`

const loginCodex = `printf '%s\n' "Sign in with your ChatGPT account. Device-code sign-in must be on for it first: ChatGPT > Settings > Security > \"Device code authorization for Codex\" (a workspace account: your workspace admin)."
exec "$rcli_bin" login --device-auth -c 'cli_auth_credentials_store="file"'
`

// Gemini CLI signs in from its interactive session. A system-settings file,
// for this session only, selects the Google sign-in; the user's own
// ~/.gemini/settings.json is not touched. It runs in an empty directory so it
// opens no project.
const loginGemini = `rcli_tmp=$(mktemp -d "${TMPDIR:-/tmp}/reliant-gemini-login.XXXXXX") || rcli_die "cannot make a temporary directory"
printf '%s\n' '{"security":{"auth":{"selectedType":"oauth-personal"}},"privacy":{"usageStatisticsEnabled":false}}' >"$rcli_tmp/system-settings.json"
printf '%s\n' "Gemini CLI opens and prints a sign-in URL. Open it, paste the code back, and type /quit once it says you are signed in."
(cd "$rcli_tmp" && NO_BROWSER=true GEMINI_CLI_SYSTEM_SETTINGS_PATH="$rcli_tmp/system-settings.json" "$rcli_bin"); rcli_rc=$?
rm -rf "$rcli_tmp"
exit "$rcli_rc"
`

// cursor-agent login prints a URL and waits for the browser sign-in;
// NO_OPEN_BROWSER stops it trying to open one on the machine.
const loginCursor = `printf '%s\n' "Sign in with your Cursor account: open the URL it prints, in any browser."
export NO_OPEN_BROWSER=1
exec "$rcli_bin" login
`

const loginCopilot = `rcli_install_gh
printf '%s\n' "GitHub Copilot CLI uses the GitHub CLI's sign-in. Sign in to github.com with the device code it prints."
exec "$rcli_gh" auth login --hostname github.com --git-protocol https --web
`

var vendors = []entry{
	{Vendor: Vendor{ID: "claude", Name: "Claude Code", Binary: "claude"},
		npmPackage: "@anthropic-ai/claude-code", npmVersion: ClaudeCodeVersion, status: statusClaude, login: loginClaude},
	{Vendor: Vendor{ID: "codex", Name: "Codex", Binary: "codex"},
		npmPackage: "@openai/codex", npmVersion: CodexVersion, status: statusCodex, login: loginCodex},
	{Vendor: Vendor{ID: "gemini", Name: "Gemini CLI", Binary: "gemini"},
		npmPackage: "@google/gemini-cli", npmVersion: GeminiCLIVersion, status: statusGemini, login: loginGemini},
	{Vendor: Vendor{ID: "cursor", Name: "Cursor Agent", Binary: "cursor-agent"},
		status: statusCursor, login: loginCursor},
	{Vendor: Vendor{ID: "copilot", Name: "GitHub Copilot CLI", Binary: "copilot"},
		npmPackage: "@github/copilot", npmVersion: CopilotVersion, status: statusCopilot, login: loginCopilot},
}

// cursorTarballs is Cursor's own agent-cli-package at CursorVersion (the
// URL its https://cursor.com/install script downloads), pinned rather than
// scraped from that script.
var cursorTarballs = []tarball{
	{os: "linux", arch: "x64", sha256: "7663b1c5d1b56e095ef8e23bef38d66df6cfd665c4873470214e0e411f0f97b3"},
	{os: "linux", arch: "arm64", sha256: "7b7dd0020fb9a2a9e1e7e3f39e2f9f512d1dd9577174ecda8aa82ac31dbc9565"},
	{os: "darwin", arch: "x64", sha256: "16cc7314128243b24d74575cae5f7e1a215656d48115d55084e2d6725ef7c542"},
	{os: "darwin", arch: "arm64", sha256: "92f39e448218897538bd801cdb01d69106ab37a86aaf3dc0e6b55159c79b75e2"},
}

// ghTarballs is the GitHub CLI release GHVersion; the hashes are its
// published gh_<v>_checksums.txt.
var ghTarballs = []tarball{
	{os: "linux", arch: "x64", sha256: "79e7ab5db738c48777b696e374f8b428f0647debfb424f44d2f426370407fe2f"},
	{os: "linux", arch: "arm64", sha256: "b1278c75107423d818f1937fd92cafc9e5e9b606631568f68d7fb816f5885a02"},
	{os: "darwin", arch: "x64", sha256: "23602cbccbbdc6285a8520745b714cb927ce2ca621779373c2a20cd8e33fda66", zip: true},
	{os: "darwin", arch: "arm64", sha256: "2a5d03103ae444927383b93d9ffd65579a26e2eb900ce1cf11f55258d5319ded", zip: true},
}

func init() {
	for i := range cursorTarballs {
		t := &cursorTarballs[i]
		t.url = fmt.Sprintf("https://downloads.cursor.com/lab/%s/%s/%s/agent-cli-package.tar.gz", CursorVersion, t.os, t.arch)
	}
	for i := range ghTarballs {
		t := &ghTarballs[i]
		goos, arch, ext := t.os, map[string]string{"x64": "amd64", "arm64": "arm64"}[t.arch], "tar.gz"
		if goos == "darwin" {
			goos = "macOS"
		}
		if t.zip {
			ext = "zip"
		}
		t.url = fmt.Sprintf("https://github.com/cli/cli/releases/download/v%s/gh_%s_%s_%s.%s", GHVersion, GHVersion, goos, arch, ext)
	}
}

// All returns every vendor, in the fixed order claude, codex, gemini,
// cursor, copilot.
func All() []Vendor {
	out := make([]Vendor, len(vendors))
	for i, e := range vendors {
		out[i] = e.Vendor
	}
	return out
}

// Lookup finds a vendor by its ID.
func Lookup(id string) (Vendor, bool) {
	e, ok := lookup(id)
	return e.Vendor, ok
}

func lookup(id string) (entry, bool) {
	for _, e := range vendors {
		if e.ID == id {
			return e, true
		}
	}
	return entry{}, false
}

// IDs returns every vendor ID, in All's order.
func IDs() []string {
	out := make([]string, len(vendors))
	for i, e := range vendors {
		out[i] = e.ID
	}
	return out
}

// linePrefix starts every line StatusScript prints.
const linePrefix = "RELIANT_CLI"

// whereLocalBin is StatusScript's optional fourth field: the CLI was found
// only in $HOME/.local/bin, which is not on the PATH the script ran with.
const whereLocalBin = "local_bin"

// statusPrelude defines the helpers every status block uses. Names are
// prefixed rcli_ so the script can be appended to another (requirements'
// probe runs under set -u with its own variables).
const statusPrelude = `rcli_t=''
if command -v timeout >/dev/null 2>&1; then rcli_t='timeout 30'; elif command -v gtimeout >/dev/null 2>&1; then rcli_t='gtimeout 30'; fi
# rcli_find BIN sets rcli_bin to the CLI on PATH, else in $HOME/.local/bin
# (and rcli_where to local_bin); it fails when there is neither.
rcli_find() {
  if rcli_bin=$(command -v "$1" 2>/dev/null) && [ -n "$rcli_bin" ]; then return 0; fi
  rcli_bin="${HOME:-}/.local/bin/$1"
  if [ -n "${HOME:-}" ] && [ -x "$rcli_bin" ]; then rcli_where=local_bin; return 0; fi
  rcli_bin=''
  return 1
}
rcli_say() {
  if [ -n "$rcli_where" ]; then printf 'RELIANT_CLI\t%s\t%s\t%s\n' "$1" "$rcli_s" "$rcli_where"
  else printf 'RELIANT_CLI\t%s\t%s\n' "$1" "$rcli_s"; fi
}
`

// StatusScript is a POSIX sh script that, for each id, prints exactly one
// line "RELIANT_CLI\t<id>\t<state>" and nothing else of the vendor's: every
// vendor command's output stays in the script (or goes to /dev/null), so no
// account data, let alone a token, crosses the wire. A CLI found only in
// $HOME/.local/bin (not on the PATH the script ran with) gets a fourth field,
// "local_bin"; ParseStatusDetail reports it. Each vendor check is bounded by
// a 30s timeout where timeout(1) exists. Unknown ids are skipped (their
// state parses as unknown); duplicates are checked once.
func StatusScript(ids []string) string {
	var sb strings.Builder
	sb.WriteString("# Reliant: vendor CLI sign-in status. Prints only RELIANT_CLI verdict lines.\n")
	sb.WriteString(statusPrelude)
	seen := map[string]bool{}
	for _, id := range ids {
		e, ok := lookup(id)
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		fmt.Fprintf(&sb, "# %s\nrcli_s=unknown; rcli_where=''; rcli_o=''\n", e.Name)
		fmt.Fprintf(&sb, "if rcli_find '%s'; then\n%selse rcli_s=missing\nfi\n", e.Binary, indent(e.status))
		fmt.Fprintf(&sb, "rcli_say '%s'\n", e.ID)
	}
	sb.WriteString("unset rcli_o\n")
	return sb.String()
}

func indent(s string) string {
	lines := strings.SplitAfter(s, "\n")
	var sb strings.Builder
	for _, l := range lines {
		if l != "" {
			sb.WriteString("  " + l)
		}
	}
	return sb.String()
}

// Detail is one CLI's verdict from StatusScript's output.
type Detail struct {
	State State
	// OffPath: the CLI was found only in $HOME/.local/bin, which is not on
	// the PATH the status script ran with. Something run with that PATH
	// (a workflow's run step, through the same exec path) cannot call it by
	// name.
	OffPath bool
}

// ParseStatusDetail reads StatusScript's output. Every vendor ID is in the
// result; one the output says nothing of is unknown. Lines that are not the
// script's verdicts are ignored.
func ParseStatusDetail(out string) map[string]Detail {
	res := make(map[string]Detail, len(vendors))
	for _, id := range IDs() {
		res[id] = Detail{State: StateUnknown}
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 3 || f[0] != linePrefix {
			continue
		}
		if _, ok := res[f[1]]; !ok {
			continue
		}
		d := Detail{State: parseState(f[2])}
		if len(f) >= 4 && f[3] == whereLocalBin {
			d.OffPath = true
		}
		res[f[1]] = d
	}
	return res
}

// ParseStatus reads StatusScript's output into each vendor's state; see
// ParseStatusDetail.
func ParseStatus(out string) map[string]State {
	details := ParseStatusDetail(out)
	res := make(map[string]State, len(details))
	for id, d := range details {
		res[id] = d.State
	}
	return res
}

func parseState(s string) State {
	switch st := State(s); st {
	case StateSignedIn, StateSignedOut, StateExpired, StateMissing:
		return st
	default:
		return StateUnknown
	}
}

// loginPrelude defines what every login script needs: where installs go,
// how to fail, how to find a CLI and install one with npm.
const loginPrelude = `set -u
rcli_die() { printf 'reliant machine login: %s\n' "$*" >&2; exit 1; }
[ -n "${HOME:-}" ] || rcli_die "HOME is not set"
rcli_share="$HOME/.local/share/reliant-cli"
rcli_lbin="$HOME/.local/bin"
rcli_find() {
  if rcli_bin=$(command -v "$1" 2>/dev/null) && [ -n "$rcli_bin" ]; then return 0; fi
  rcli_bin="$rcli_lbin/$1"
  [ -x "$rcli_bin" ] && return 0
  rcli_bin=''
  return 1
}
rcli_link() {
  mkdir -p "$rcli_lbin" || rcli_die "cannot create $rcli_lbin"
  ln -sfn "$1" "$rcli_lbin/$2" || rcli_die "cannot link $rcli_lbin/$2"
  rcli_bin="$rcli_lbin/$2"
  case ":${PATH:-}:" in *":$rcli_lbin:"*) ;; *) printf '%s\n' "Note: $rcli_lbin is not on this PATH; $2 is at $rcli_bin." ;; esac
}
# rcli_npm ID PACKAGE VERSION BIN: install PACKAGE@VERSION under $HOME.
rcli_npm() {
  command -v npm >/dev/null 2>&1 || rcli_die "npm (Node.js) is not installed on this machine, so $4 cannot be installed: install Node.js, then run this again"
  printf 'Installing %s@%s into %s\n' "$2" "$3" "$rcli_share/$1"
  mkdir -p "$rcli_share/$1" || rcli_die "cannot create $rcli_share/$1"
  npm_config_update_notifier=false npm install --no-audit --no-fund --loglevel=error --prefix "$rcli_share/$1" "$2@$3" || rcli_die "npm install $2@$3 failed"
  [ -e "$rcli_share/$1/node_modules/.bin/$4" ] || rcli_die "npm installed $2 but there is no $4 in $rcli_share/$1/node_modules/.bin"
  rcli_link "$rcli_share/$1/node_modules/.bin/$4" "$4"
}
`

// downloadPrelude defines how a login script downloads and verifies a pinned
// tarball (cursor, and gh for copilot).
const downloadPrelude = `rcli_platform() {
  case "$(uname -s)" in Linux*) rcli_os=linux ;; Darwin*) rcli_os=darwin ;; *) rcli_die "unsupported operating system: $(uname -s)" ;; esac
  case "$(uname -m)" in x86_64|amd64) rcli_arch=x64 ;; arm64|aarch64) rcli_arch=arm64 ;; *) rcli_die "unsupported architecture: $(uname -m)" ;; esac
}
rcli_fetch() {
  if command -v curl >/dev/null 2>&1; then curl -fsSL -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then wget -q -O "$2" "$1"
  else rcli_die "neither curl nor wget is installed on this machine, so the download cannot run"
  fi
}
rcli_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else rcli_die "neither sha256sum nor shasum is installed on this machine, so the download cannot be verified"
  fi
}
# rcli_download URL SHA256 FILE: download and verify, or fail.
rcli_download() {
  rcli_fetch "$1" "$3" || rcli_die "downloading $1 failed"
  rcli_got=$(rcli_sha256 "$3")
  [ "$rcli_got" = "$2" ] || rcli_die "$1 has sha256 $rcli_got, not the pinned $2: not installing it"
}
`

// LoginScript is a sh script run in a PTY on the target machine, as the
// user the machine's daemon runs as: install the CLI if it is missing (at
// the pinned version, under $HOME, linked into $HOME/.local/bin), then exec
// the vendor's own login. The vendor writes its own files; nothing of the
// sign-in passes through Reliant beyond the terminal the person types in.
func LoginScript(id string) (string, error) {
	e, ok := lookup(id)
	if !ok {
		return "", UnknownVendorError(id)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Reliant: sign %s in on this machine with its own login.\n", e.Name)
	sb.WriteString(loginPrelude)
	if e.ID == "cursor" || e.ID == "copilot" {
		sb.WriteString(downloadPrelude)
	}
	if e.ID == "copilot" {
		sb.WriteString(installGHFunc())
	}
	fmt.Fprintf(&sb, "if ! rcli_find '%s'; then\n", e.Binary)
	switch {
	case e.npmPackage != "":
		fmt.Fprintf(&sb, "  rcli_npm '%s' '%s' '%s' '%s'\n", e.ID, e.npmPackage, e.npmVersion, e.Binary)
	case e.ID == "cursor":
		sb.WriteString(indent(installCursor()))
	}
	sb.WriteString("fi\n")
	sb.WriteString(e.login)
	return sb.String(), nil
}

// installCursor installs Cursor's agent-cli-package as its own install script
// lays it out (the package's top directory stripped), under rcli_share.
func installCursor() string {
	var sb strings.Builder
	sb.WriteString("rcli_platform\n")
	sb.WriteString(tarballCase("cursor", cursorTarballs))
	fmt.Fprintf(&sb, `rcli_dest="$rcli_share/cursor/%s"
if [ ! -x "$rcli_dest/cursor-agent" ]; then
  printf 'Installing Cursor Agent %s into %%s\n' "$rcli_dest"
  mkdir -p "$rcli_share/cursor" || rcli_die "cannot create $rcli_share/cursor"
  rcli_tmp=$(mktemp -d "$rcli_share/cursor/.download.XXXXXX") || rcli_die "cannot make a download directory"
  rcli_download "$rcli_url" "$rcli_sha" "$rcli_tmp/package.$rcli_ext"
  mkdir -p "$rcli_tmp/pkg" && tar --strip-components=1 -xzf "$rcli_tmp/package.$rcli_ext" -C "$rcli_tmp/pkg" || { rm -rf "$rcli_tmp"; rcli_die "unpacking Cursor Agent failed"; }
  rm -rf "$rcli_dest" && mv "$rcli_tmp/pkg" "$rcli_dest" || { rm -rf "$rcli_tmp"; rcli_die "cannot install into $rcli_dest"; }
  rm -rf "$rcli_tmp"
fi
rcli_link "$rcli_dest/cursor-agent" cursor-agent
`, CursorVersion, CursorVersion)
	return sb.String()
}

// installGHFunc defines rcli_install_gh, which sets rcli_gh to the GitHub
// CLI, installing the pinned release under rcli_share when there is none.
func installGHFunc() string {
	var sb strings.Builder
	sb.WriteString("rcli_install_gh() {\n")
	sb.WriteString("  rcli_save=$rcli_bin\n")
	sb.WriteString("  if rcli_find gh; then rcli_gh=$rcli_bin; rcli_bin=$rcli_save; return 0; fi\n")
	sb.WriteString("  rcli_platform\n")
	sb.WriteString(indent(tarballCase("gh", ghTarballs)))
	fmt.Fprintf(&sb, `  rcli_dest="$rcli_share/gh/%s"
  if [ ! -x "$rcli_dest/bin/gh" ]; then
    printf 'Installing the GitHub CLI %s into %%s\n' "$rcli_dest"
    mkdir -p "$rcli_share/gh" || rcli_die "cannot create $rcli_share/gh"
    rcli_tmp=$(mktemp -d "$rcli_share/gh/.download.XXXXXX") || rcli_die "cannot make a download directory"
    rcli_download "$rcli_url" "$rcli_sha" "$rcli_tmp/gh.$rcli_ext"
    mkdir -p "$rcli_tmp/pkg" || rcli_die "cannot make $rcli_tmp/pkg"
    if [ "$rcli_ext" = zip ]; then
      command -v unzip >/dev/null 2>&1 || { rm -rf "$rcli_tmp"; rcli_die "unzip is not installed on this machine, so the GitHub CLI cannot be unpacked"; }
      unzip -q "$rcli_tmp/gh.zip" -d "$rcli_tmp/zip" && mv "$rcli_tmp"/zip/*/* "$rcli_tmp/pkg/" || { rm -rf "$rcli_tmp"; rcli_die "unpacking the GitHub CLI failed"; }
    else
      tar --strip-components=1 -xzf "$rcli_tmp/gh.tar.gz" -C "$rcli_tmp/pkg" || { rm -rf "$rcli_tmp"; rcli_die "unpacking the GitHub CLI failed"; }
    fi
    rm -rf "$rcli_dest" && mv "$rcli_tmp/pkg" "$rcli_dest" || { rm -rf "$rcli_tmp"; rcli_die "cannot install into $rcli_dest"; }
    rm -rf "$rcli_tmp"
  fi
  rcli_link "$rcli_dest/bin/gh" gh
  rcli_gh=$rcli_bin
  rcli_bin=$rcli_save
}
`, GHVersion, GHVersion)
	return sb.String()
}

// tarballCase is sh that sets rcli_url, rcli_sha and rcli_ext for the
// machine's platform (rcli_os, rcli_arch), or fails naming it.
func tarballCase(what string, tbs []tarball) string {
	var sb strings.Builder
	sb.WriteString("case \"$rcli_os/$rcli_arch\" in\n")
	for _, t := range tbs {
		ext := "tar.gz"
		if t.zip {
			ext = "zip"
		}
		fmt.Fprintf(&sb, "  %s/%s) rcli_url='%s'; rcli_sha='%s'; rcli_ext='%s' ;;\n", t.os, t.arch, t.url, t.sha256, ext)
	}
	fmt.Fprintf(&sb, "  *) rcli_die \"no %s build for $rcli_os/$rcli_arch\" ;;\nesac\n", what)
	return sb.String()
}

// UnknownVendorError is the error for an ID that is not a vendor: it lists
// the valid ones.
type UnknownVendorError string

func (e UnknownVendorError) Error() string {
	return fmt.Sprintf("%q is not a vendor CLI (%s)", string(e), strings.Join(IDs(), ", "))
}
