// Copyright (c) 2025 Reliant Labs
package candidate

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
)

// fileStats lists every file whose entry differs between two commits (the
// merge base and the head of a paths job), as GitHub's pull request files
// list reports them: a status, line counts, and the blob at b.
//
// Rename detection is on (-M, git's default 50% similarity, which is what
// GitHub's list detects renames with), so a moved file is one renamed entry
// carrying its PreviousPath rather than a removal and an addition. Copy
// detection is off, as in a plain `git diff`. Past git's rename limit (1000
// candidates) git skips rename detection, and a moved file is listed as a
// removal and an addition: both paths are still listed, so nothing the risk
// tier reads is lost. A binary file has no line counts (numstat's "-"): it
// is listed with 0 and 0, as GitHub lists it, and marked Binary.
//
// It is one diff-tree with both the raw and numstat formats, NUL-terminated,
// so the two halves come from one rename pairing. --full-index makes the raw
// format's object ids whole.
func (g localGit) fileStats(ctx context.Context, a, b string) ([]buildjob.FileStat, error) {
	out, _, err := g.run(ctx, "", "", "diff-tree", "-r", "-z", "-M", "--raw", "--numstat", "--full-index", "--no-commit-id", a, b)
	if err != nil {
		return nil, err
	}
	return parseFileStats(out)
}

// parseFileStats reads `diff-tree -z --raw --numstat` output. Every raw
// record comes first, then every numstat record (git's diff_flush order),
// but each record is recognized by its own shape, so the order is not
// relied on:
//
//	raw:      ":<old mode> <new mode> <old id> <new id> <status>\0<path>\0"
//	          (R and C: "...\0<old path>\0<new path>\0")
//	numstat:  "<added>\t<deleted>\t<path>\0"
//	          (a rename: "<added>\t<deleted>\t\0<old path>\0<new path>\0")
func parseFileStats(out string) ([]buildjob.FileStat, error) {
	tokens := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if len(tokens) == 1 && tokens[0] == "" {
		return nil, nil
	}
	type count struct {
		add, del int
		binary   bool
	}
	var files []buildjob.FileStat
	counts := map[string]count{}
	take := func(i, n int) ([]string, error) {
		if i+n > len(tokens) {
			return nil, fmt.Errorf("candidate: diff-tree output ends inside a record (%q)", tokens[i-1])
		}
		return tokens[i : i+n], nil
	}
	for i := 0; i < len(tokens); {
		t := tokens[i]
		if strings.HasPrefix(t, ":") {
			fields := strings.Fields(t[1:])
			if len(fields) != 5 || fields[4] == "" {
				return nil, fmt.Errorf("candidate: diff-tree raw record %q", t)
			}
			status, ok := rawStatus(fields[4][0])
			if !ok {
				return nil, fmt.Errorf("candidate: diff-tree status %q", fields[4])
			}
			f := buildjob.FileStat{Status: status, BlobSHA: fields[3]}
			n := 1
			if status == buildjob.FileRenamed || status == buildjob.FileCopied {
				n = 2
			}
			paths, err := take(i+1, n)
			if err != nil {
				return nil, err
			}
			f.Path = paths[n-1]
			if n == 2 {
				f.PreviousPath = paths[0]
			}
			files = append(files, f)
			i += 1 + n
			continue
		}
		parts := strings.SplitN(t, "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("candidate: diff-tree numstat record %q", t)
		}
		path := parts[2]
		i++
		if path == "" { // a rename: the old and new paths follow
			paths, err := take(i, 2)
			if err != nil {
				return nil, err
			}
			path = paths[1]
			i += 2
		}
		var c count
		if parts[0] == "-" && parts[1] == "-" {
			c.binary = true
		} else {
			add, aerr := strconv.Atoi(parts[0])
			del, derr := strconv.Atoi(parts[1])
			if aerr != nil || derr != nil || add < 0 || del < 0 {
				return nil, fmt.Errorf("candidate: diff-tree numstat record %q", t)
			}
			c.add, c.del = add, del
		}
		counts[path] = c
	}
	for i := range files {
		c, ok := counts[files[i].Path]
		if !ok {
			return nil, fmt.Errorf("candidate: diff-tree counted no lines for %s", files[i].Path)
		}
		files[i].Additions, files[i].Deletions, files[i].Binary = c.add, c.del, c.binary
	}
	slices.SortFunc(files, func(x, y buildjob.FileStat) int { return strings.Compare(x.Path, y.Path) })
	return files, nil
}

// rawStatus is a raw record's status letter in GitHub's words.
func rawStatus(letter byte) (buildjob.FileStatus, bool) {
	switch letter {
	case 'A':
		return buildjob.FileAdded, true
	case 'D':
		return buildjob.FileRemoved, true
	case 'M':
		return buildjob.FileModified, true
	case 'R':
		return buildjob.FileRenamed, true
	case 'C':
		return buildjob.FileCopied, true
	case 'T':
		return buildjob.FileChanged, true
	}
	return "", false
}
