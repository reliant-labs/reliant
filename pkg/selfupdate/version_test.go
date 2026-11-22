package selfupdate

import (
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	cases := []struct {
		name         string
		cur          Build
		rel          Release
		newer, known bool
	}{
		{"semver newer", Build{Version: "1.2.3"}, Release{Version: "1.3.0"}, true, true},
		{"v prefix mixes", Build{Version: "v1.2.3"}, Release{Version: "1.2.4"}, true, true},
		{"older", Build{Version: "1.2.3"}, Release{Version: "1.2.2"}, false, true},
		{"same", Build{Version: "1.2.3"}, Release{Version: "v1.2.3"}, false, true},
		{"prerelease before release", Build{Version: "1.3.0-rc.1"}, Release{Version: "1.3.0"}, true, true},
		{"semver beats commit time", Build{Version: "1.3.0", CommitTime: t0}, Release{Version: "1.2.0", CommitTime: t1}, false, true},
		{"equal semver, later commit", Build{Version: "1.3.0", CommitTime: t0}, Release{Version: "1.3.0", CommitTime: t1}, true, true},
		{"dev build ordered by commit time", Build{Version: "dev", CommitTime: t0}, Release{Version: "1.0.0", CommitTime: t1}, true, true},
		{"dev build with no time is never upgraded", Build{Version: "dev"}, Release{Version: "1.0.0"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, k := newer(tc.cur, tc.rel)
			if n != tc.newer || k != tc.known {
				t.Fatalf("newer = %v, %v; want %v, %v", n, k, tc.newer, tc.known)
			}
		})
	}
}
