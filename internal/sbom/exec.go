package sbom

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// execRunner runs the real syft binary.
type execRunner struct{}

// Run execs bin with args. syft's own update check is switched off: a cut
// must not phone a third party, and the version forge runs is pinned anyway.
// stderr is kept for the error only; with -q a successful run writes none.
// env goes on the child alone (exec dedups keys, the last one wins), so a
// credential in it never enters forge's own environment.
func (execRunner) Run(ctx context.Context, bin string, args, env []string) ([]byte, error) {
	cmd := syftCommand(ctx, bin, args, env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// syftCommand is the syft process: forge's environment, the update check
// off, then env. args never carry a credential (syftArgs builds them).
func syftCommand(ctx context.Context, bin string, args, env []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // bin is forge's pinned syft; args are built by syftArgs
	cmd.Env = append(append(os.Environ(), "SYFT_CHECK_FOR_APP_UPDATE=false"), env...)
	return cmd
}
