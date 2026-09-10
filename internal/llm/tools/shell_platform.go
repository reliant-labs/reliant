// Copyright (c) 2025 Reliant Labs
package tools

// ShellToolName is the shell tool's name on every platform.
//
// It is deliberately NOT platform-specific. The name is persisted in tool_calls
// rows and referenced by workflow tool filters, so a per-OS name would change
// underneath a chat that moves between daemons and would break every stored
// reference. The PLATFORM difference belongs in the description, which is
// rebuilt per request; see shellDescription.
const ShellToolName = "shell"

// ShellPlatform is the shell family of the machine that will actually RUN a
// shell command — the daemon, which may be a different OS from the server that
// builds the tool description.
//
// It is not the server's runtime.GOOS. Resolving it from the server's own
// compile-time identity is the bug this type exists to prevent: a Linux server
// would tell the model "uses bash -c" while a Windows daemon fed the command to
// PowerShell, which does not fail cleanly — it mangles bash syntax mid-run.
type ShellPlatform string

const (
	// ShellPlatformUnknown means no daemon platform was resolved. Descriptions
	// degrade to portable guidance rather than assuming bash.
	ShellPlatformUnknown ShellPlatform = ""
	// ShellPlatformUnix is bash on macOS/Linux/BSD.
	ShellPlatformUnix ShellPlatform = "unix"
	// ShellPlatformWindows is PowerShell on Windows.
	ShellPlatformWindows ShellPlatform = "windows"
)

// unixGOOS is the set of GOOS values a daemon can plausibly report that run a
// bash-family shell. It is an allowlist rather than "anything that is not
// windows" on purpose: an unrecognized or garbage value must degrade to
// ShellPlatformUnknown and get portable guidance, not be confidently told it
// has bash.
var unixGOOS = map[string]bool{
	"darwin": true, "linux": true, "freebsd": true, "openbsd": true,
	"netbsd": true, "dragonfly": true, "solaris": true, "illumos": true,
	"aix": true, "android": true, "ios": true,
}

// ShellPlatformFromGOOS maps a daemon-reported runtime.GOOS string (persisted
// as daemons.platform) to a shell family.
func ShellPlatformFromGOOS(goos string) ShellPlatform {
	switch {
	case goos == "windows":
		return ShellPlatformWindows
	case unixGOOS[goos]:
		return ShellPlatformUnix
	default:
		return ShellPlatformUnknown
	}
}

// shellDescription returns the shell tool's description for the platform that
// will execute the command. Selected at request time from the daemon record —
// never from the server's build tags.
func shellDescription(platform ShellPlatform) string {
	return shellPlatformPreamble(platform) + shellDescriptionCommon()
}

func shellPlatformPreamble(platform ShellPlatform) string {
	switch platform {
	case ShellPlatformWindows:
		return `Execute PowerShell commands for building, testing, and system operations in a stateless shell.

# ⚠️ THE EXECUTING MACHINE RUNS WINDOWS — WRITE POWERSHELL, NOT BASH
Commands are run with ` + "`powershell -NoProfile -NonInteractive -Command`" + `.
Bash syntax does not fail cleanly here; PowerShell reinterprets it, so a bash
one-liner tends to half-run and produce confusing output rather than an error.
- Chain with ` + "`;`" + `. ` + "`&&`" + ` and ` + "`||`" + ` are not PowerShell operators.
- Redirect stderr with ` + "`2>&1`" + ` only as PowerShell spells it; ` + "`>/dev/null`" + ` is ` + "`> $null`" + `.
- Variables are ` + "`$env:NAME`" + `, not ` + "`$NAME`" + `. Globs and quoting differ from POSIX.
- Paths use ` + "`\\`" + ` and drive letters. Unix tools (` + "`ls`" + `, ` + "`grep`" + `, ` + "`cat`" + `) may be absent —
  prefer ` + "`Get-ChildItem`" + `, ` + "`Select-String`" + `, ` + "`Get-Content`" + `, and check with ` + "`Get-Command`" + `
  before relying on a POSIX binary.`
	case ShellPlatformUnix:
		return `Execute bash commands for building, testing, and system operations in a stateless shell.

Uses bash -c to execute commands on Unix/macOS/Linux.`
	default:
		return `Execute shell commands for building, testing, and system operations in a stateless shell.

# ⚠️ THE EXECUTING MACHINE'S PLATFORM IS NOT KNOWN — DO NOT ASSUME BASH
Commands run on a remote machine whose operating system has not been reported
to this session, so it may be a POSIX shell (bash -c) or PowerShell on Windows.
Write portable commands, and PROBE before relying on either dialect:
- Run one cheap command first (` + "`uname -s`" + `, or ` + "`$PSVersionTable`" + `) and read the
  result to find out which shell you actually have, then commit to that dialect.
- Until you know: prefer one command per call over chaining, avoid ` + "`&&`" + `, ` + "`||`" + `,
  ` + "`$VAR`" + ` expansion, POSIX redirection and shell globbing, all of which mean
  different things (or nothing) in PowerShell.
- Do not guess path separators; use paths exactly as they were given to you.`
	}
}
