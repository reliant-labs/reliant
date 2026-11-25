package doctor

// toolchain.go — Go toolchain health checks.
//
// `task coverage` (and any project-side use of `-covermode=atomic
// -coverprofile=...`) shells out to `go tool covdata` for merging /
// summarising coverage profiles. A subset of auto-installed Go
// toolchains (notably the v0.0.1-go1.26.2 module-cache toolchain) ship
// without `covdata` built — `go tool covdata` returns
// `go: no such tool "covdata"` and the module-cache directory is
// read-only so the binary can't be built post-hoc.
//
// CheckCovdata warns (not fails) when the active toolchain is
// missing covdata, and points at the `go install` workaround. It's
// intentionally a `warn` rather than `fail` because the absence is
// only material to projects that opted into coverage tooling — the
// scaffolded `task coverage` recipe doesn't use `-covermode=atomic`
// for exactly this reason, so most projects won't notice.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/reliant-labs/forge/internal/goexec"
)

// CheckCovdata verifies that `go tool covdata` is available in the
// active Go toolchain. Returns a warning (not a failure) with an
// install hint when the tool is missing.
//
// The probe is `go tool covdata` with NO selector. Exit status alone
// cannot answer the question: covdata has no zero-arg success mode, so a
// PRESENT covdata exits 2 ("error: missing command selector") exactly as
// an ABSENT one does ("go: no such tool"). The previous probe ran
// `go tool covdata help` — `help` is not one of covdata's selectors
// either, so a perfectly healthy toolchain fell through to the catch-all
// and doctor warned `go tool covdata probe failed: exit status 2` on
// every run, naming no fix for a problem that did not exist. Presence is
// decided on the output sentinel, not the exit code.
func CheckCovdata(ctx context.Context, _ *Environment) CheckResult {
	cmd := exec.CommandContext(ctx, "go", "tool", "covdata")
	cmd.Env = goexec.Env()
	out, err := cmd.CombinedOutput()
	body := string(out)
	// covdata printing its own usage banner IS the proof it exists,
	// whatever it exits with.
	if err == nil || strings.Contains(body, "usage: go tool covdata") {
		return CheckResult{
			Status:  StatusPass,
			Message: "go tool covdata available",
		}
	}

	// Distinguish "tool genuinely missing" from "go itself isn't on
	// PATH" — the latter is a much louder problem and the user has
	// other signals for it.
	if errors.Is(err, exec.ErrNotFound) {
		return CheckResult{
			Status:   StatusFail,
			Message:  "go binary not found on PATH",
			Evidence: err.Error(),
		}
	}

	if strings.Contains(body, "no such tool") && strings.Contains(body, "covdata") {
		return CheckResult{
			Status: StatusWarn,
			Message: "go tool covdata missing — `task coverage` with " +
				"-covermode=atomic will fail; install with " +
				"`go install golang.org/x/tools/cmd/covdata@latest`",
			Evidence: strings.TrimSpace(body),
		}
	}

	// Some other `go tool` failure (cancellation, malformed install,
	// permissions). The probe did not answer its question — covdata is
	// neither shown present nor shown missing — so this is UNDETERMINED
	// rather than a warning about a known problem. Same reasoning as
	// grafanaAddr in telemetry.go: forge could not look. The captured
	// output rides along so the user can diagnose (see the StatusSkip vs
	// StatusUnknown note in doctor.go).
	return CheckResult{
		Status:   StatusUnknown,
		Message:  fmt.Sprintf("go tool covdata probe failed, so its availability is unknown: %v", err),
		Evidence: strings.TrimSpace(body),
	}
}
