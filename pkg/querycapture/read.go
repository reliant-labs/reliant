package querycapture

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Capture is every capture file of a directory, merged.
type Capture struct {
	// Files is how many *.jsonl files were read.
	Files int `json:"files"`
	// Records is how many records were merged.
	Records int `json:"records"`
	// Skipped counts lines that were not a record this reader understands: a
	// line cut short by a process killed mid-write, or a record of a newer
	// SchemaVersion.
	Skipped int `json:"skipped"`
	// Shapes are the distinct statements, by fingerprint.
	Shapes []Shape `json:"shapes"`
}

// Shape is one statement shape and every place it was run from.
type Shape struct {
	Fingerprint string `json:"fingerprint"`
	// SQL is the first-read record's SQL. Records of one fingerprint differ
	// at most in the inline literals the fingerprint masks.
	SQL       string   `json:"sql"`
	Operation string   `json:"operation,omitempty"`
	Tables    []string `json:"tables,omitempty"`
	// Callers, sorted by File then Line.
	Callers []CallSite `json:"callers"`
	// Count is how many records were merged into the shape: one per process
	// per caller that ran it, not how many times it executed.
	Count int `json:"count"`
}

// CallSite is one caller of a shape.
type CallSite struct {
	Caller    Frame  `json:"caller"`
	Generated *Frame `json:"generated,omitempty"`
	// Tests are the distinct test functions recorded for this caller, sorted.
	Tests []string `json:"tests,omitempty"`
	// Count is how many records named this caller.
	Count int `json:"count"`
}

// Read merges every *.jsonl capture file in dir. Shapes come out sorted by
// fingerprint, so two reads of one directory are identical. A dir that does
// not exist is an error; an empty one is an empty Capture.
func Read(dir string) (*Capture, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	c := &Capture{Shapes: []Shape{}}
	byFP := map[string]*Shape{}
	var order []string
	for _, p := range paths {
		if err := c.readFile(p, byFP, &order); err != nil {
			return nil, err
		}
	}
	sort.Strings(order)
	for _, fp := range order {
		s := byFP[fp]
		sort.Slice(s.Callers, func(i, j int) bool {
			a, b := s.Callers[i].Caller, s.Callers[j].Caller
			if a.File != b.File {
				return a.File < b.File
			}
			return a.Line < b.Line
		})
		c.Shapes = append(c.Shapes, *s)
	}
	return c, nil
}

func (c *Capture) readFile(path string, byFP map[string]*Shape, order *[]string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	c.Files++
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil || rec.Version < 1 || rec.Version > SchemaVersion || rec.Fingerprint == "" {
			c.Skipped++
			continue
		}
		c.Records++
		s, ok := byFP[rec.Fingerprint]
		if !ok {
			s = &Shape{Fingerprint: rec.Fingerprint, SQL: rec.SQL, Operation: rec.Operation, Tables: rec.Tables}
			byFP[rec.Fingerprint] = s
			*order = append(*order, rec.Fingerprint)
		}
		s.Count++
		s.addCaller(rec)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func (s *Shape) addCaller(rec Record) {
	for i := range s.Callers {
		cs := &s.Callers[i]
		if sameFrame(cs.Caller, rec.Caller) {
			cs.Count++
			if rec.Test != "" && !slices.Contains(cs.Tests, rec.Test) {
				cs.Tests = append(cs.Tests, rec.Test)
				sort.Strings(cs.Tests)
			}
			if cs.Generated == nil {
				cs.Generated = rec.Generated
			}
			return
		}
	}
	cs := CallSite{Caller: rec.Caller, Generated: rec.Generated, Count: 1}
	if rec.Test != "" {
		cs.Tests = []string{rec.Test}
	}
	s.Callers = append(s.Callers, cs)
}

// sameFrame compares call sites by position: the absolute path when both
// have one (two modules can each have internal/db/x.go), else the
// module-relative path.
func sameFrame(a, b Frame) bool {
	if a.Line != b.Line {
		return false
	}
	if a.AbsFile != "" && b.AbsFile != "" {
		return a.AbsFile == b.AbsFile
	}
	return a.File == b.File && a.Module == b.Module
}
