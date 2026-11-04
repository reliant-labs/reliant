package pgtest

import "testing"

// TestShmIDFromPidfile pins the postmaster.pid parse that the shm-reclaim
// reaper depends on: the segment id is the second field of the 7th line
// (LOCK_FILE_LINE_SHMEM_KEY, "<shmkey> <shmid>"). Getting this wrong would
// silently disable the leak fix, so the happy path and every reject path are
// asserted directly on the parser.
func TestShmIDFromPidfile(t *testing.T) {
	// A realistic lock file: pid, datadir, start-time, port, socketdir,
	// listen addr, "<shmkey>   <shmid>", status. The id (2949121) is the
	// second number on line 7.
	const pidfile = "53687\n/tmp/forge-pgtest/59982/data\n1718857908\n59982\n/tmp\nlocalhost\n  379676230   2949121\nready   \n"
	if id, ok := shmIDFromPidfile(pidfile); !ok || id != 2949121 {
		t.Fatalf("shmIDFromPidfile(valid) = (%d, %v), want (2949121, true)", id, ok)
	}

	rejects := map[string]string{
		"empty":                "",
		"too few lines":        "123\n/data\n",
		"no segment recorded":  "1\n/d\n2\n3\n/s\nhost\n0 0\nready\n", // "0 0" -> id<=0
		"shmem line one field": "1\n/d\n2\n3\n/s\nhost\n379676230\nready\n",
		"non-numeric id":       "1\n/d\n2\n3\n/s\nhost\nkey shmid\nready\n",
	}
	for name, content := range rejects {
		if id, ok := shmIDFromPidfile(content); ok {
			t.Errorf("shmIDFromPidfile(%s) = (%d, true), want ok=false", name, id)
		}
	}
}
