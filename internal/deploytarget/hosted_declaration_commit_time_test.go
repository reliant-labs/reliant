package deploytarget

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// The hosted wire carries commitTime as an RFC 3339 UTC string (proto3 JSON
// for google.protobuf.Timestamp), and omits it when unset so an older control
// plane and a dirty checkout see the same request they always did.
func TestProvenanceWireFields_CommitTime(t *testing.T) {
	commit := strings.Repeat("a", 40)
	ct := time.Date(2026, 3, 4, 10, 30, 0, 0, time.FixedZone("x", 5*3600+1800))

	with := ProvenanceWireFields(release.Provenance{Commit: commit, CommitTime: &ct})
	if got := with["commitTime"]; got != "2026-03-04T05:00:00Z" {
		t.Errorf("commitTime = %v, want 2026-03-04T05:00:00Z", got)
	}

	without := ProvenanceWireFields(release.Provenance{Commit: commit})
	if _, present := without["commitTime"]; present {
		t.Errorf("an unset commit time must be omitted: %v", without)
	}
}
