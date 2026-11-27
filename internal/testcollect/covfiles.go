package testcollect

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// The two kinds of file a -cover binary writes (runtime/coverage):
//
//	covmeta.<hash>                    the build's meta-data, one per build
//	covcounters.<hash>.<pid>.<nanos>  one process's counters, written at
//	                                  exit or by WriteCountersDir
const (
	metaPrefix    = "covmeta."
	counterPrefix = "covcounters."
)

// maxTarBytes bounds one pulled tar. A meta file covering 315 packages and
// 205k statements is 1.15 MB gzipped (phase 0); counters are smaller still.
const maxTarBytes = 512 << 20

// covFile is one coverage file, from wherever it was read.
type covFile struct {
	name string
	data []byte
	hash string
	// meta: a covmeta file; otherwise a counter file, written at nanos.
	meta  bool
	nanos int64
	// live: pulled from a running process; started is when it started
	// (a pod's start time), which orders live builds.
	live    bool
	started time.Time
	// scope tells its process apart for the reset baseline (baseline.go):
	// "host" for a host workload's pull and exit flush, the pod for a pod's
	// pull, "" where no baseline applies (compose).
	scope string
}

// parseCovName reads a coverage file name. ok is false for any other file.
func parseCovName(name string) (f covFile, ok bool) {
	switch {
	case strings.HasPrefix(name, metaPrefix):
		h := strings.TrimPrefix(name, metaPrefix)
		if !isHex(h) {
			return covFile{}, false
		}
		return covFile{name: name, hash: h, meta: true}, true
	case strings.HasPrefix(name, counterPrefix):
		parts := strings.Split(strings.TrimPrefix(name, counterPrefix), ".")
		if len(parts) != 3 || !isHex(parts[0]) {
			return covFile{}, false
		}
		if _, err := strconv.ParseUint(parts[1], 10, 64); err != nil {
			return covFile{}, false
		}
		nanos, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return covFile{}, false
		}
		return covFile{name: name, hash: parts[0], nanos: nanos}, true
	}
	return covFile{}, false
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// counterName is the name Go gives a counter file.
func counterName(hash, pid string, nanos int64) string {
	return fmt.Sprintf("%s%s.%s.%d", counterPrefix, hash, pid, nanos)
}

// readTar returns the coverage files at the top of a tar: serverkit's is
// flat, and `docker cp <c>:<dir>/. -` names the directory's contents "./x".
// top, when set, also accepts the contents one level under a directory of
// that name (`docker cp <c>:/tmp -` names them "tmp/x"). Anything else in
// the tar, deeper files included, is ignored.
func readTar(r io.Reader, top string) ([]covFile, error) {
	lr := &io.LimitedReader{R: r, N: maxTarBytes + 1}
	tr := tar.NewReader(lr)
	var out []covFile
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read coverage tar: %w", err)
		}
		if lr.N <= 0 {
			return nil, fmt.Errorf("coverage tar exceeds %d MiB", maxTarBytes>>20)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		clean := strings.TrimPrefix(path.Clean(strings.TrimPrefix(hdr.Name, "/")), "./")
		if dir := path.Dir(clean); dir != "." && (top == "" || dir != top) {
			continue // deeper than the GOCOVERDIR itself
		}
		f, ok := parseCovName(path.Base(clean))
		if !ok {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read coverage tar: %s: %w", hdr.Name, err)
		}
		if lr.N <= 0 {
			return nil, fmt.Errorf("coverage tar exceeds %d MiB", maxTarBytes>>20)
		}
		f.data = data
		out = append(out, f)
	}
	return out, nil
}

// readDir returns the coverage files directly in dir. A missing dir has
// none.
func readDir(dir string) ([]covFile, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []covFile
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		f, ok := parseCovName(e.Name())
		if !ok {
			continue
		}
		if f.data, err = os.ReadFile(filepath.Join(dir, e.Name())); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// build is one binary build's files: its meta and every counter file of it.
type build struct {
	hash     string
	meta     []byte
	hasMeta  bool
	counters []covFile
}

// newest reports the build's recency: a live pull beats any flushed file,
// and among live pulls the newest process wins.
func (b *build) newest() (live bool, started time.Time, nanos int64) {
	for _, c := range b.counters {
		if c.live {
			live = true
			if c.started.After(started) {
				started = c.started
			}
		}
		nanos = max(nanos, c.nanos)
	}
	return live, started, nanos
}

// groupBuilds groups files by covmeta hash, the build fingerprint: the
// counters of one binary build, only. It returns the builds that have both
// a meta file and counters, newest first, and the hashes of counters whose
// meta file is missing (which no tool can read).
func groupBuilds(files []covFile) (builds []*build, orphans []string) {
	by := map[string]*build{}
	get := func(h string) *build {
		if b, ok := by[h]; ok {
			return b
		}
		b := &build{hash: h}
		by[h] = b
		return b
	}
	for _, f := range files {
		b := get(f.hash)
		if f.meta {
			b.meta, b.hasMeta = f.data, true
			continue
		}
		b.counters = append(b.counters, f)
	}
	for _, b := range by {
		switch {
		case len(b.counters) == 0:
			// Meta with no counters (all of them before a reset): no data.
		case !b.hasMeta:
			orphans = append(orphans, b.hash)
		default:
			builds = append(builds, b)
		}
	}
	sort.Strings(orphans)
	sort.Slice(builds, func(i, j int) bool {
		li, si, ni := builds[i].newest()
		lj, sj, nj := builds[j].newest()
		switch {
		case li != lj:
			return li
		case !si.Equal(sj):
			return si.After(sj)
		case ni != nj:
			return ni > nj
		}
		return builds[i].hash < builds[j].hash
	})
	return builds, orphans
}

// write writes b's meta and counters into dir, which it creates. Two
// counter files can arrive under one name (two pods, each pid 1, flushing
// in the same nanosecond); the later one is renamed a nanosecond on, since
// the name is all a reader sorts or keys by.
func (b *build) write(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, metaPrefix+b.hash), b.meta, 0o644); err != nil {
		return err
	}
	written := map[string][]byte{}
	for _, c := range b.counters {
		name := c.name
		for {
			prev, taken := written[name]
			if !taken {
				break
			}
			if bytes.Equal(prev, c.data) {
				name = ""
				break
			}
			pid := strings.Split(strings.TrimPrefix(c.name, counterPrefix), ".")[1]
			c.nanos++
			name = counterName(b.hash, pid, c.nanos)
		}
		if name == "" {
			continue // the same file, read from two sources
		}
		written[name] = c.data
		if err := atomicfile.WriteFile(filepath.Join(dir, name), c.data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// counterFiles is how many distinct counter files b holds.
func (b *build) counterFiles() int {
	seen := map[string]bool{}
	for _, c := range b.counters {
		seen[c.name+"\x00"+string(c.data)] = true
	}
	return len(seen)
}
