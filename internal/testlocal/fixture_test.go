package testlocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testreport"
)

// The fixtures write run directories into t.TempDir() and answer git through
// a fake, so nothing here execs git or go.

const (
	fxCommit = "1111111111111111111111111111111111111111"
	fxTree   = "2222222222222222222222222222222222222222"
	fxBase   = "3333333333333333333333333333333333333333"
	fxModule = "example.com/shop"
)

// fxProject is a project root with a go.mod and the given source files.
func fxProject(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files["go.mod"] = "module " + fxModule + "\n\ngo 1.24\n"
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

type fxLane struct {
	name     string
	exit     int
	format   testreport.LaneFormat
	results  string // contents of the results file; "" for none
	cover    string // contents of cover.out; "" for none
	errorMsg string
}

type fxRun struct {
	id      string
	created time.Time
	commit  string
	tree    string
	dirty   bool
	branch  string
	base    string
	lanes   []fxLane
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fxWriteRun writes one run directory under root and returns it.
func fxWriteRun(t *testing.T, root string, r fxRun) string {
	t.Helper()
	dir := filepath.Join(testreport.RunsRoot(root), r.id)
	m := testreport.RunManifest{
		Schema: testreport.RunManifestSchema, ID: r.id, CreatedAt: r.created,
		Commit: r.commit, TreeHash: r.tree, Dirty: r.dirty, Branch: r.branch, BaseSHA: r.base,
		Project: "shop", Lanes: []string{},
	}
	for _, l := range r.lanes {
		m.Lanes = append(m.Lanes, l.name)
		ldir := filepath.Join(dir, l.name)
		lm := testreport.LaneManifest{
			Schema: testreport.LaneManifestSchema, Lane: l.name, Command: []string{"go", "test"},
			StartedAt: r.created, FinishedAt: r.created, ExitCode: l.exit, Error: l.errorMsg,
			Format: l.format, Results: testreport.LaneResultsMissing, Coverage: []testreport.LaneCoverageInput{},
		}
		if lm.Format == "" {
			lm.Format = testreport.LaneFormatGoJSON
		}
		if err := os.MkdirAll(ldir, 0o755); err != nil {
			t.Fatal(err)
		}
		if l.results != "" {
			name := testreport.LaneResultsFile
			if lm.Format == testreport.LaneFormatJUnit {
				name = "junit.xml"
			}
			if err := os.WriteFile(filepath.Join(ldir, name), []byte(l.results), 0o644); err != nil {
				t.Fatal(err)
			}
			lm.Results, lm.ResultsFile = testreport.LaneResultsParsed, name
		}
		if l.cover != "" {
			if err := os.WriteFile(filepath.Join(ldir, testreport.LaneCoverProfile), []byte(l.cover), 0o644); err != nil {
				t.Fatal(err)
			}
			lm.Coverage = append(lm.Coverage, testreport.LaneCoverageInput{Path: testreport.LaneCoverProfile, Format: testreport.CoverageGoProfile})
		}
		writeJSON(t, filepath.Join(ldir, testreport.LaneManifestFile), lm)
	}
	writeJSON(t, filepath.Join(dir, testreport.RunManifestFile), m)
	return dir
}

// goJSON builds a `go test -json` stream: each case is "pkg:Test:status".
func goJSON(cases ...string) string {
	var b strings.Builder
	pkgs := map[string]string{}
	var order []string
	for _, c := range cases {
		f := strings.SplitN(c, ":", 3)
		pkg, name, status := f[0], f[1], f[2]
		if _, ok := pkgs[pkg]; !ok {
			order = append(order, pkg)
			pkgs[pkg] = "pass"
		}
		fmt.Fprintf(&b, `{"Action":"run","Package":%q,"Test":%q}`+"\n", pkg, name)
		if status == "fail" {
			fmt.Fprintf(&b, `{"Action":"output","Package":%q,"Test":%q,"Output":"    x_test.go:9: boom\n"}`+"\n", pkg, name)
			pkgs[pkg] = "fail"
		}
		if status == "skip" {
			fmt.Fprintf(&b, `{"Action":"output","Package":%q,"Test":%q,"Output":"    x_test.go:3: no db\n"}`+"\n", pkg, name)
		}
		fmt.Fprintf(&b, `{"Action":%q,"Package":%q,"Test":%q,"Elapsed":0}`+"\n", status, pkg, name)
	}
	for _, p := range order {
		fmt.Fprintf(&b, `{"Action":%q,"Package":%q,"Elapsed":0.1}`+"\n", pkgs[p], p)
	}
	return b.String()
}

// fakeGit answers the git reads the package makes, for a repository at top
// whose tree (and commit) hold blobs, with a diff of diff from base.
type fakeGit struct {
	top   string
	blobs map[string]string
	head  string
	diff  string
	calls []string
}

func (g *fakeGit) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	g.calls = append(g.calls, strings.Join(args, " "))
	switch args[0] {
	case "rev-parse":
		if args[1] == "--show-toplevel" {
			if g.top == "" {
				return nil, errors.New("not a git repository")
			}
			return []byte(g.top + "\n"), nil
		}
		rev := strings.TrimSuffix(args[len(args)-1], "^{commit}")
		switch rev {
		case "HEAD":
			return []byte(g.head + "\n"), nil
		case "origin/main", "main", fxBase:
			return []byte(fxBase + "\n"), nil
		case fxCommit:
			return []byte(fxCommit + "\n"), nil
		}
		return nil, fmt.Errorf("unknown revision %s", rev)
	case "symbolic-ref":
		return []byte("origin/main\n"), nil
	case "merge-base":
		return []byte(fxBase + "\n"), nil
	case "ls-tree":
		treeish := args[len(args)-1]
		if treeish != fxTree && treeish != fxCommit {
			return nil, fmt.Errorf("fatal: not a tree object: %s", treeish)
		}
		var b strings.Builder
		for p, id := range g.blobs {
			fmt.Fprintf(&b, "100644 blob %s\t%s\x00", id, p)
		}
		return []byte(b.String()), nil
	case "ls-files":
		var b strings.Builder
		for p, id := range g.blobs {
			fmt.Fprintf(&b, "100644 %s 0\t%s\x00", id, p)
		}
		return []byte(b.String()), nil
	case "diff":
		if args[1] == "--name-only" {
			return nil, nil
		}
		return []byte(g.diff), nil
	}
	return nil, fmt.Errorf("fake git: unexpected %v", args)
}

// blobsOf hashes the project's files as git would.
func blobsOf(t *testing.T, root string, paths ...string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		m[p] = testreport.GitBlobID(b)
	}
	return m
}
