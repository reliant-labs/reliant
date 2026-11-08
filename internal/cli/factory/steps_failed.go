package factory

import (
	"fmt"
	"strings"
)

// StepsFailedError is what a generate pipeline run returns when it ran to the
// end but one or more of its steps failed. Each failure was reported where it
// happened, the steps that do not depend on it ran, the steps that do said
// they were skipped, and every write the run made stands: unlike a failure
// that aborts the run, nothing was rolled back.
//
// It is still an error. Agents, scripts and CI read exit 0 as success, and
// this run did not produce everything it should have, so `forge generate`
// exits non-zero. A caller distinguishes it with errors.As only to choose a
// different consequence, never to swallow it: `--watch` keeps watching, and a
// scaffold keeps the sources it wrote (its own rollback would delete the
// sources the run's standing writes were generated from) before failing.
type StepsFailedError struct {
	// Steps names each failed step, in the order they failed.
	Steps []string
}

func (e *StepsFailedError) Error() string {
	return fmt.Sprintf("%d generate step(s) failed: %s (the other steps ran and their output stands; the errors are above)",
		len(e.Steps), strings.Join(e.Steps, ", "))
}
