package lint

import "io"

// formatGoTree is the Go formatter over the whole tree, verified by the real
// type-check: what `forge lint --fix`'s pre-pass applies (structuredFixPrePass).
func formatGoTree(root string) ([]string, error) {
	return formatGoTreeFiltered(root, nil)
}

// formatGoTreeFiltered is formatGoTree restricted to the files keep accepts,
// as a scoped `forge lint --fix` runs it.
func formatGoTreeFiltered(root string, keep func(rel string) bool) ([]string, error) {
	changed, _, err := formatGoTreeVerified(root, keep, fixTypecheck)
	return changed, err
}

// reportLintVerdict renders the final line of a run from what it proved.
func reportLintVerdict(w io.Writer, ranGating, skippedGating, advisoryUnavailable []string) error {
	return lintVerdict{ran: ranGating, skipped: skippedGating, advisoryUnavailable: advisoryUnavailable}.render(w)
}
