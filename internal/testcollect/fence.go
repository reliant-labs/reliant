package testcollect

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// The reset fence: what `forge test collect --reset` excludes from every
// later collect of one workload.
//
// A reset starts a run's coverage from zero, and the rule is:
//
//   - Live counters are baselined (baseline.go): each reachable process's
//     counts at the reset are recorded and subtracted from what it later
//     reports, pulled live or flushed when it exits.
//   - Flushed counters are fenced off by IDENTITY: every counter file present
//     at the moment of the reset is recorded in the workload's reset record
//     (reset.go) as (source, name, sha256 of its bytes), and a later collect leaves a file out only when all three
//     match an entry. The source is where the file was read: the workload's
//     host GOCOVERDIR, or one compose container. This includes the file of a
//     process that exited before the reset and was never collected: it counts
//     the time before the run began.
//   - Anything else is collected. Go names a counter file
//     covcounters.<build>.<pid>.<nanos>, so two processes or containers can
//     write files of one name (pid 1 in every container); a file that shares a
//     fenced file's name but comes from another source, or holds other bytes,
//     is a different file and is kept. A name's encoded timestamp is never
//     compared with anything, so no clock can drop a file the reset did not
//     see.
//   - The fence only grows. A later reset adds what it sees to what is already
//     there, so a file excluded once is never re-included.
//   - A reset that zeroed nothing (refused, or nothing answered) writes nothing,
//     so the next collect keeps everything it would have kept before.
//
// The files are listed after the baseline pull. A process that exits in
// between leaves a file, which is then excluded whole: the reset errs towards
// counting nothing from before it.

// fenceEntry is one fenced counter file.
type fenceEntry struct {
	// Source is where the file was read: "dir:<path relative to the project>"
	// for a host GOCOVERDIR, "container:<id>" for a compose container.
	Source string `json:"source"`
	Name   string `json:"name"`
	// SHA256 is the hex sha256 of the file's bytes.
	SHA256 string `json:"sha256"`
}

func (e fenceEntry) less(o fenceEntry) int {
	return strings.Compare(e.Source+"\x00"+e.Name+"\x00"+e.SHA256, o.Source+"\x00"+o.Name+"\x00"+o.SHA256)
}

// fenceSet is the fence read for lookups. The zero value fences nothing.
type fenceSet map[fenceEntry]bool

// excludes reports whether the fence leaves out the counter file f read from
// source. Meta files are never fenced: they carry no counts.
func (s fenceSet) excludes(source string, f covFile) bool {
	return !f.meta && s[entryOf(source, f)]
}

func entryOf(source string, f covFile) fenceEntry {
	sum := sha256.Sum256(f.data)
	return fenceEntry{Source: source, Name: f.name, SHA256: hex.EncodeToString(sum[:])}
}

// dirSource and containerSource name the sources a fence entry records.
func dirSource(projectRoot, dir string) string { return "dir:" + rel(projectRoot, dir) }
func containerSource(id string) string         { return "container:" + id }

// fenceOut drops the counter files read from source that the fence excludes.
func fenceOut(files []covFile, s fenceSet, source string) []covFile {
	if len(s) == 0 {
		return files
	}
	out := files[:0:0]
	for _, f := range files {
		if !s.excludes(source, f) {
			out = append(out, f)
		}
	}
	return out
}

// fenceEntries are the fence entries of the counter files among files, all
// read from source.
func fenceEntries(source string, files []covFile) []fenceEntry {
	var out []fenceEntry
	for _, f := range files {
		if !f.meta {
			out = append(out, entryOf(source, f))
		}
	}
	return out
}
