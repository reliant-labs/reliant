package cli

import (
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// The hosted ImportLedger resolves a promotion's release from the same
// request. A release the control plane already holds (a deploy records its
// release there before the import runs) must therefore still travel with a
// promotion that names it, or the promotion is refused: "names release X,
// which this import does not include".
func TestReleasesToSend_KeepsAHeldReleaseANewPromotionNames(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	src := ledgerSource{
		Releases: []sourceRelease{
			{Release: release.Release{Version: "v1"}},
			{Release: release.Release{Version: "v2"}},
			{Release: release.Release{Version: "v3"}},
		},
		Promotions: map[string][]sourcePromotion{"prod": {
			{Promotion: release.Promotion{ID: "old", Env: "prod", Release: "v1", PromotedAt: at}},
			{Promotion: release.Promotion{ID: "new", Env: "prod", Release: "v2", PromotedAt: at}},
		}},
	}
	held := ledgerHeld{
		ReleaseVersions: map[string]struct{}{"v1": {}, "v2": {}},
		PromotionIDs:    map[string]map[string]struct{}{"prod": {"old": {}}},
	}

	send, alreadyHeld := releasesToSend(src, []string{"prod"}, held)

	got := map[string]bool{}
	for _, r := range send {
		got[r.Release.Version] = true
	}
	if !got["v2"] {
		t.Errorf("v2 is held but promotion %q (not yet imported) names it; it must be re-sent, sent %v", "new", got)
	}
	if got["v1"] {
		t.Errorf("v1 is held and every promotion naming it is imported; it must be subtracted, sent %v", got)
	}
	if !got["v3"] {
		t.Errorf("v3 is not held; it must be sent, sent %v", got)
	}
	if alreadyHeld != 2 {
		t.Errorf("alreadyHeld = %d, want 2 (v1 and v2 are both held)", alreadyHeld)
	}
}
