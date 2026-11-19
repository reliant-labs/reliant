// Package exitcode is the one place an error becomes a process status.
//
// forge's agent verbs print a document whose exit_code is "exactly the status
// the process will exit with". That promise holds only if the document's
// stamp and the process's os.Exit read the SAME resolution, so both call Of.
// A host that embeds forge (reliant forge) must call it too: exiting 1 for
// every error discards the 2 ("could not look") a gate branches on.
package exitcode

import "errors"

// Of is the process status err produces: 0 for nil; the code of the first
// error in the chain that reports a non-zero ExitCode() int; otherwise 1,
// "we looked, and it is wrong". An unclassified failure is never 2: that is
// the one answer CI treats as retryable.
func Of(err error) int {
	if err == nil {
		return 0
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		if c := coded.ExitCode(); c != 0 {
			return c
		}
	}
	return 1
}
