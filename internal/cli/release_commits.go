package cli

// THE COMMIT RANGE A RELEASE ADDS, recorded at the cut (console v3, D4).
//
// The promote plan already lists the commits arriving with a promote, but it
// computes them from git ancestry at PROMOTE time, on the promoting machine —
// which a control plane, a console, or a teammate's checkout of another branch
// cannot repeat. So the cut, the one moment a checkout is guaranteed to be at
// the release's commit, records the range against the newest earlier release
// it descends from (release.CaptureCommitRange), and every reader composes
// ranges from the ledger instead (release.CompareCommits).
//
// BEST-EFFORT, LIKE PROVENANCE. A range that cannot be captured — git missing,
// the ledger unreadable, the capture budget spent — is recorded as absent and
// said once on stderr. It never fails a cut: a release with no range is still
// exactly the release it was, and refusing it would trade a description for
// the deploy.

import (
	"context"
	"fmt"
	"io"

	"github.com/reliant-labs/forge/pkg/release"
)

// captureReleaseCommits is the range rel adds over the release it was cut
// after, or nil when none can be recorded. A var so a test states the range
// rather than standing up a repository.
var captureReleaseCommits = func(ctx context.Context, projectDir string, rel release.Release, releases releaseLedger, warn io.Writer) *release.CommitRange {
	if rel.Provenance == nil || rel.Provenance.Commit == "" {
		return nil
	}
	listed, err := releases.List(ctx)
	if err != nil {
		// Without the earlier releases there is no base, and a range with
		// none would read as "everything since the first commit".
		fmt.Fprintf(warn, "[release] Note: %s records no commit range: the earlier releases in %s could not be listed: %v\n",
			rel.Version, releases.Location(), err)
		return nil
	}
	earlier := make([]release.Release, 0, len(listed))
	for _, r := range listed {
		if r.Version == rel.Version || (r.Project != "" && rel.Project != "" && r.Project != rel.Project) {
			continue
		}
		earlier = append(earlier, r)
	}
	commits, err := release.CaptureCommitRange(ctx, projectDir, rel.Provenance.Commit, earlier)
	if err != nil {
		fmt.Fprintf(warn, "[release] Note: %s records no commit range: %v\n", rel.Version, err)
		return nil
	}
	return commits
}
