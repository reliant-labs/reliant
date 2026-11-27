package testlocal

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testreport"
)

// A run directory may be an artifact of code nobody has reviewed (CI's
// upload job downloads one and reads it holding credentials), so its
// capture.json is hostile input. These plant a sentinel OUTSIDE the run and
// point the manifest at it every way a path can escape, and require the
// sentinel never to be read.

const confineSentinel = "SENTINEL-OUTSIDE-THE-LANE"

// confineOutside writes the sentinel files outside any run: a go-json
// results stream and a Go cover profile that would parse if read, and a
// directory of covdata files.
func confineOutside(t *testing.T) (dir, results, cover, covdata string) {
	t.Helper()
	dir = t.TempDir()
	results = filepath.Join(dir, "environ")
	stream := `{"Action":"run","Package":"example.com/shop","Test":"TestX"}` + "\n" +
		`{"Action":"output","Package":"example.com/shop","Test":"TestX","Output":"` + confineSentinel + `\n"}` + "\n" +
		`{"Action":"fail","Package":"example.com/shop","Test":"TestX","Elapsed":0}` + "\n" +
		`{"Action":"fail","Package":"example.com/shop","Elapsed":0.1}` + "\n"
	cover = filepath.Join(dir, "cover.out")
	covdata = filepath.Join(dir, "covdata")
	for p, body := range map[string]string{
		results:                              stream,
		cover:                                "mode: atomic\nexample.com/shop/" + confineSentinel + ".go:1.1,2.2 1 1\n",
		filepath.Join(covdata, "covmeta.aa"): confineSentinel,
		filepath.Join(covdata, "covcounters.aa.1.2"): confineSentinel,
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, results, cover, covdata
}

func confineLane(t *testing.T) *Lane {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run", "unit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Lane{Dir: dir, LaneManifest: testreport.LaneManifest{
		Schema: testreport.LaneManifestSchema, Lane: "unit", Format: testreport.LaneFormatGoJSON,
		Results: testreport.LaneResultsParsed,
	}}
}

func confineSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating a symbolic link needs a privilege this host lacks: %v", err)
		}
		t.Fatal(err)
	}
}

func TestLaneNeverReadsAResultsFileOutsideIt(t *testing.T) {
	cases := map[string]func(t *testing.T, l *Lane, outsideDir, results string) string{
		"dot-dot traversal": func(t *testing.T, l *Lane, _, results string) string {
			rel, err := filepath.Rel(l.Dir, results)
			if err != nil {
				t.Fatal(err)
			}
			return filepath.ToSlash(rel)
		},
		"absolute path": func(_ *testing.T, _ *Lane, _, results string) string { return results },
		"proc-style absolute path": func(_ *testing.T, _ *Lane, _, _ string) string {
			return "../../../../../../../../proc/self/environ"
		},
		"symbolic link to a file outside": func(t *testing.T, l *Lane, _, results string) string {
			confineSymlink(t, results, filepath.Join(l.Dir, "results.json"))
			return "results.json"
		},
		"symbolic link to a directory outside": func(t *testing.T, l *Lane, outsideDir, _ string) string {
			confineSymlink(t, outsideDir, filepath.Join(l.Dir, "sub"))
			return "sub/environ"
		},
	}
	for name, point := range cases {
		t.Run(name, func(t *testing.T) {
			outsideDir, results, _, _ := confineOutside(t)
			l := confineLane(t)
			l.ResultsFile = point(t, l, outsideDir, results)

			if p := l.ResultsPath(); p != "" {
				t.Fatalf("ResultsPath = %q for a file outside the lane", p)
			}
			if why := l.ResultsProblem(); !strings.Contains(why, "outside the lane directory") {
				t.Fatalf("ResultsProblem = %q, want it to say the path is outside the lane", why)
			}
			if f, err := l.OpenResults(); err == nil {
				_ = f.Close()
				t.Fatal("OpenResults opened a file outside the lane")
			}
			res, why := readResults(t.TempDir(), l)
			if res != nil || strings.Contains(why, confineSentinel) || !strings.Contains(why, "outside the lane directory") {
				t.Fatalf("readResults = %+v, %q", res, why)
			}
			a := &Assembly{Lanes: []*Lane{l}}
			if s := a.LaneStreams(); len(s) != 0 {
				t.Fatalf("LaneStreams lists a file outside the lane: %+v", s)
			}
		})
	}
}

func TestLaneReadsItsOwnResultsFile(t *testing.T) {
	l := confineLane(t)
	stream := goJSON(fxModule + ":TestA:pass")
	if err := os.WriteFile(filepath.Join(l.Dir, "results.json"), []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	// A link that stays inside the lane is the lane's own file.
	confineSymlink(t, "results.json", filepath.Join(l.Dir, "alias.json"))
	for _, name := range []string{"results.json", "alias.json"} {
		l.ResultsFile = name
		if l.ResultsPath() == "" || l.ResultsProblem() != "" {
			t.Fatalf("%s: path %q, problem %q", name, l.ResultsPath(), l.ResultsProblem())
		}
		res, why := readResults(t.TempDir(), l)
		if res == nil || why != "" || res.Totals.Passed != 1 {
			t.Fatalf("%s: results %+v, %q", name, res, why)
		}
	}
}

func TestLaneNeverReadsCoverageOutsideIt(t *testing.T) {
	outsideDir, _, cover, covdata := confineOutside(t)
	l := confineLane(t)
	rel, err := filepath.Rel(l.Dir, cover)
	if err != nil {
		t.Fatal(err)
	}
	relCovdata, err := filepath.Rel(l.Dir, covdata)
	if err != nil {
		t.Fatal(err)
	}
	confineSymlink(t, cover, filepath.Join(l.Dir, "linked-cover.out"))
	confineSymlink(t, covdata, filepath.Join(l.Dir, "linked-covdata"))
	// A real covdata directory whose files link out: only its own real
	// files may reach `go tool covdata`.
	own := filepath.Join(l.Dir, "covdata")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "covmeta.bb"), []byte("own-meta"), 0o644); err != nil {
		t.Fatal(err)
	}
	confineSymlink(t, filepath.Join(covdata, "covcounters.aa.1.2"), filepath.Join(own, "covcounters.bb.1.2"))
	confineSymlink(t, outsideDir, filepath.Join(own, "covmeta.dir"))

	l.Coverage = []testreport.LaneCoverageInput{
		{Path: filepath.ToSlash(rel), Format: testreport.CoverageGoProfile},
		{Path: cover, Format: testreport.CoverageGoProfile},
		{Path: "linked-cover.out", Format: testreport.CoverageGoProfile},
		{Path: filepath.ToSlash(relCovdata) + "/", Format: testreport.CoverageGoCovdata},
		{Path: covdata + "/", Format: testreport.CoverageGoCovdata},
		{Path: "linked-covdata/", Format: testreport.CoverageGoCovdata},
		{Path: "covdata/", Format: testreport.CoverageGoCovdata},
	}
	var converted []string
	opts := Options{ProjectRoot: t.TempDir(), Covdata: func(_ context.Context, _, dir string) ([]testreport.CovdataBuild, error) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			if strings.Contains(string(b), confineSentinel) {
				t.Errorf("covdata conversion was handed %s, read from outside the lane", e.Name())
			}
			converted = append(converted, e.Name()+"="+string(b))
		}
		return nil, nil
	}}
	docs, notes, err := readCoverage(context.Background(), opts, l, "", &Assembly{})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 0 {
		t.Fatalf("coverage read from outside the lane: %+v", docs)
	}
	refused := 0
	for _, n := range notes {
		if strings.Contains(n, confineSentinel) {
			t.Fatalf("a note carries the sentinel: %q", n)
		}
		if strings.Contains(n, "outside the lane directory") {
			refused++
		}
	}
	if refused != 6 {
		t.Fatalf("%d inputs refused as outside the lane, want 6:\n%s", refused, strings.Join(notes, "\n"))
	}
	if strings.Join(converted, ",") != "covmeta.bb=own-meta" {
		t.Fatalf("covdata handed to the conversion: %v, want only the lane's own real file", converted)
	}
}

func TestLaneProbeSkipsConventionalNamesLinkingOut(t *testing.T) {
	_, _, cover, _ := confineOutside(t)
	l := confineLane(t)
	confineSymlink(t, cover, filepath.Join(l.Dir, testreport.LaneCoverProfile))
	if in := l.CoverageInputs(); len(in) != 0 {
		t.Fatalf("CoverageInputs lists a file outside the lane: %+v", in)
	}
}

func TestOpenSkipsManifestsThatAreLinks(t *testing.T) {
	outsideDir := t.TempDir()
	writeJSON(t, filepath.Join(outsideDir, "capture.json"), testreport.LaneManifest{
		Schema: testreport.LaneManifestSchema, Lane: "unit", ResultsFile: confineSentinel,
	})
	writeJSON(t, filepath.Join(outsideDir, "run.json"), testreport.RunManifest{
		Schema: testreport.RunManifestSchema, ID: confineSentinel, Commit: fxCommit,
	})
	run := filepath.Join(t.TempDir(), "01RUN")
	writeJSON(t, filepath.Join(run, "real", testreport.LaneManifestFile), testreport.LaneManifest{
		Schema: testreport.LaneManifestSchema, Lane: "real",
	})
	confineSymlink(t, filepath.Join(outsideDir, "capture.json"), filepath.Join(run, "unit", testreport.LaneManifestFile))
	confineSymlink(t, outsideDir, filepath.Join(run, "linked"))
	confineSymlink(t, filepath.Join(outsideDir, "run.json"), filepath.Join(run, testreport.RunManifestFile))

	r, err := Open(run)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID == confineSentinel || r.Commit != "" {
		t.Fatalf("run.json read through a link: %+v", r.RunManifest)
	}
	if len(r.Lanes) != 1 || r.Lanes[0].Lane != "real" {
		t.Fatalf("lanes %+v, want only the real one", r.Lanes)
	}
	if !strings.Contains(strings.Join(r.Problems, "\n"), "lane unit: capture.json is not a regular file") {
		t.Fatalf("problems do not name the linked capture.json: %q", r.Problems)
	}
}
