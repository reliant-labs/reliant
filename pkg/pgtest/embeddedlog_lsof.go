//go:build !linux && !windows

package pgtest

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ownOpenPaths lists the files this process has a descriptor on, with
// `lsof -p <pid>`: there is no /proc here. Any failure is an error, and the
// caller then claims nothing (the log is left for the stale sweep) rather than
// guess.
func ownOpenPaths() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "lsof", "-p", strconv.Itoa(os.Getpid()), "-Fn", "-w") // #nosec G204 -- fixed arguments plus our own pid
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		// Exit 1 is lsof's routine "some file could not be listed".
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || len(out) == 0 {
			return nil, err
		}
	}
	var paths []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		if line := sc.Text(); len(line) > 1 && line[0] == 'n' && line[1] == '/' {
			paths = append(paths, line[1:])
		}
	}
	return paths, sc.Err()
}
