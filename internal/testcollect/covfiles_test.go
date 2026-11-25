package testcollect

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestParseCovName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ok    bool
		meta  bool
		hash  string
		nanos int64
	}{
		{"covmeta.0a1b", true, true, "0a1b", 0},
		{"covcounters.0a1b.42.1700000000000000001", true, false, "0a1b", 1700000000000000001},
		{"covcounters.0a1b.42", false, false, "", 0},
		{"covcounters.XYZ.42.1", false, false, "", 0},
		{"covmeta.", false, false, "", 0},
		{"covmeta.0a1b.tmp", false, false, "", 0},
		{"profile.out", false, false, "", 0},
	} {
		f, ok := parseCovName(tc.name)
		if ok != tc.ok || (ok && (f.meta != tc.meta || f.hash != tc.hash || f.nanos != tc.nanos)) {
			t.Errorf("parseCovName(%q) = %+v, %v; want ok=%v meta=%v hash=%q nanos=%d", tc.name, f, ok, tc.ok, tc.meta, tc.hash, tc.nanos)
		}
	}
}

// tarOf builds a tar of name→body entries, in order.
func tarOf(t *testing.T, entries ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := 0; i < len(entries); i += 2 {
		name, body := entries[i], entries[i+1]
		typ := byte(tar.TypeReg)
		if body == "<dir>" {
			typ, body = tar.TypeDir, ""
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: typ, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// serverkit's tar is flat; `docker cp <c>:/tmp/. -` nests the contents under
// "./". Only the coverage files at the top of the directory are read.
func TestReadTarKeepsOnlyTheDirectorysCoverageFiles(t *testing.T) {
	data := tarOf(t,
		"./", "<dir>",
		"./covmeta.aa", "meta",
		"./covcounters.aa.1.10", "c1",
		"covcounters.aa.2.20", "c2",
		"./go-build123/covmeta.bb", "nested",
		"./hsperf/x", "unrelated",
		"./notes.txt", "unrelated",
		"tmp/covcounters.aa.3.30", "under the dir's own name",
	)
	names := func(top string) []string {
		fs, err := readTar(bytes.NewReader(data), top)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range fs {
			out = append(out, f.name)
		}
		return out
	}
	if got, want := names(""), []string{"covmeta.aa", "covcounters.aa.1.10", "covcounters.aa.2.20"}; !slices.Equal(got, want) {
		t.Errorf("flat: read %v, want %v", got, want)
	}
	if got, want := names("tmp"), []string{"covmeta.aa", "covcounters.aa.1.10", "covcounters.aa.2.20", "covcounters.aa.3.30"}; !slices.Equal(got, want) {
		t.Errorf("under tmp/: read %v, want %v", got, want)
	}
	if _, err := readTar(bytes.NewReader(bytes.Repeat([]byte("not a tar "), 200)), ""); err == nil {
		t.Error("garbage read as a tar")
	}
}

// One workload's binary builds are grouped by covmeta hash: a live pull is
// the current build whatever its age, flushed builds rank by when their
// newest counters were written, and counters without a meta file are named.
func TestGroupBuilds(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	files := []covFile{
		{name: "covmeta.aa", hash: "aa", meta: true, data: []byte("A")},
		{name: "covcounters.aa.1.100", hash: "aa", nanos: 100},
		{name: "covmeta.bb", hash: "bb", meta: true, data: []byte("B")},
		{name: "covcounters.bb.1.900", hash: "bb", nanos: 900},
		{name: "covmeta.cc", hash: "cc", meta: true, data: []byte("C")}, // no counters
		{name: "covcounters.dd.1.5", hash: "dd", nanos: 5},              // no meta
	}
	builds, orphans := groupBuilds(files)
	if len(builds) != 2 || builds[0].hash != "bb" || builds[1].hash != "aa" {
		t.Fatalf("builds %v, want bb (newest counters) then aa", hashes(builds))
	}
	if len(orphans) != 1 || orphans[0] != "dd" {
		t.Errorf("orphans %v, want [dd]", orphans)
	}

	live := append(files, covFile{name: "covcounters.aa.7.50", hash: "aa", nanos: 50, live: true, started: t0})
	if builds, _ := groupBuilds(live); builds[0].hash != "aa" {
		t.Errorf("builds %v: the live process's build must be current", hashes(builds))
	}

	pods := []covFile{
		{name: "covmeta.aa", hash: "aa", meta: true},
		{name: "covcounters.aa.1.10", hash: "aa", nanos: 10, live: true, started: t0},
		{name: "covmeta.bb", hash: "bb", meta: true},
		{name: "covcounters.bb.1.5", hash: "bb", nanos: 5, live: true, started: t0.Add(time.Minute)},
	}
	if builds, _ := groupBuilds(pods); builds[0].hash != "bb" {
		t.Errorf("builds %v: the newest pod's build must be current", hashes(builds))
	}
}

func hashes(bs []*build) []string {
	var out []string
	for _, b := range bs {
		out = append(out, b.hash)
	}
	return out
}

// Two pods' counter files can share a name (pid 1, one nanosecond); both
// are kept.
func TestBuildWriteKeepsCollidingCounterFiles(t *testing.T) {
	b := &build{hash: "aa", meta: []byte("M"), counters: []covFile{
		{name: "covcounters.aa.1.10", hash: "aa", nanos: 10, data: []byte("pod-a")},
		{name: "covcounters.aa.1.10", hash: "aa", nanos: 10, data: []byte("pod-b")},
		{name: "covcounters.aa.1.10", hash: "aa", nanos: 10, data: []byte("pod-a")}, // read twice
	}}
	dir := filepath.Join(t.TempDir(), "w")
	if err := b.write(dir); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"covcounters.aa.1.10", "covcounters.aa.1.11", "covmeta.aa"}
	if len(names) != 3 || names[0] != want[0] || names[1] != want[1] || names[2] != want[2] {
		t.Errorf("wrote %v, want %v", names, want)
	}
	if n := b.counterFiles(); n != 2 {
		t.Errorf("counterFiles = %d, want 2", n)
	}
}

func TestReadDirReadsTheCoverageFiles(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"covmeta.aa":           "M",
		"covcounters.aa.1.100": "old",
		"covcounters.aa.2.300": "new",
		"something-else.json":  "x",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fs, err := readDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range fs {
		names = append(names, f.name)
	}
	if want := []string{"covcounters.aa.1.100", "covcounters.aa.2.300", "covmeta.aa"}; !slices.Equal(names, want) {
		t.Fatalf("read %v, want %v", names, want)
	}
	if fs, err := readDir(filepath.Join(dir, "missing")); err != nil || fs != nil {
		t.Errorf("a missing dir: %v %v, want nothing and no error", fs, err)
	}
}
