package forgedelegate

import "testing"

// The cache keeps more builds than a busy machine runs distinct pins, so one
// project's resolution never evicts another's in-use forge. Below that, every
// session on the machine rebuilt some project's forge (the 2026-11-04 /tmp
// exhaustion: 13 pins against a cache of 4).
func TestKeepVersionsExceedsTheMeasuredPinsInUse(t *testing.T) {
	const pinsInUse = 13 // distinct forge pins on one shared cloud machine, 2026-11-04
	if keepVersions < pinsInUse {
		t.Errorf("keepVersions = %d, below the %d pins one shared machine resolves at once; the cache thrashes",
			keepVersions, pinsInUse)
	}
}
