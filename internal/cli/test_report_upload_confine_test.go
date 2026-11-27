package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testreport"
)

// CI's upload job reads run directories that unreviewed code wrote, holding
// an OIDC request credential in its environment. A capture.json naming a
// file outside its lane (`../../proc/self/environ`) must not get that file
// read into the report, or uploaded: each case here plants a sentinel
// outside the project and points the lane at it.
func TestTestReportUploadNeverSendsAFileOutsideTheLane(t *testing.T) {
	const sentinel = "SENTINEL-OIDC-REQUEST-TOKEN"
	stream := `{"Action":"run","Package":"example.com/m/a","Test":"TestF"}` + "\n" +
		`{"Action":"output","Package":"example.com/m/a","Test":"TestF","Output":"` + sentinel + `\n"}` + "\n" +
		`{"Action":"pass","Package":"example.com/m/a","Test":"TestF","Elapsed":0}` + "\n" +
		`{"Action":"pass","Package":"example.com/m/a","Elapsed":0.1}` + "\n"
	cover := "mode: atomic\nexample.com/m/a/a.go:3.14,5.2 1 1\nexample.com/m/" + sentinel + ".go:1.1,2.2 1 1\n"

	type point func(t *testing.T, laneDir, file string) string
	rel := func(t *testing.T, laneDir, file string) string {
		r, err := filepath.Rel(laneDir, file)
		if err != nil {
			t.Fatal(err)
		}
		return filepath.ToSlash(r)
	}
	abs := func(_ *testing.T, _, file string) string { return file }
	link := func(name string) point {
		return func(t *testing.T, laneDir, file string) string {
			p := filepath.Join(laneDir, name)
			_ = os.Remove(p)
			if err := os.Symlink(file, p); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("creating a symbolic link needs a privilege this host lacks: %v", err)
				}
				t.Fatal(err)
			}
			return name
		}
	}
	for _, tc := range []struct {
		name     string
		coverage bool // the escape is the coverage input's, not the results file's
		point    point
	}{
		{"results file climbs out with ..", false, rel},
		{"results file is absolute", false, abs},
		{"results file links out", false, link("results.json")},
		{"coverage climbs out with ..", true, rel},
		{"coverage is absolute", true, abs},
		{"coverage links out", true, link("cover.out")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeTestsService{features: []string{hostedTestFeatureResults}}
			root := trUploadProject(t, trStream("pass"), 0, fake, nil)
			outside := t.TempDir()
			file := filepath.Join(outside, "environ")
			body := stream
			if tc.coverage {
				body = cover
			}
			if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			laneDir := filepath.Join(testreport.RunsRoot(root), "01RUN", "unit")
			capture := filepath.Join(laneDir, testreport.LaneManifestFile)
			m, err := testreport.ReadLaneManifest(filepath.Dir(laneDir), "unit")
			if err != nil {
				t.Fatal(err)
			}
			if tc.coverage {
				m.Coverage = []testreport.LaneCoverageInput{{Path: tc.point(t, laneDir, file), Format: testreport.CoverageGoProfile}}
			} else {
				m.ResultsFile = tc.point(t, laneDir, file)
			}
			trJSON(t, capture, m)

			rep, _, stderr := trReportStderr(t, "--upload")
			doc, err := json.Marshal(rep)
			if err != nil {
				t.Fatal(err)
			}
			seen := string(doc) + "\n" + stderr
			for _, b := range fake.recorded() {
				all, _ := json.Marshal(b)
				seen += "\n" + string(all)
				for _, part := range []string{"results", "coverage", "diff"} {
					if _, ok := b[part].(map[string]any); ok {
						_, data := gunzipPart(t, b, part)
						seen += "\n" + string(data)
					}
				}
			}
			if strings.Contains(seen, sentinel) {
				t.Fatalf("a file outside the lane was read into the report or the upload:\n%s", seen)
			}
			if !strings.Contains(seen, "outside the lane directory") {
				t.Fatalf("nothing says the path outside the lane was refused:\n%s", seen)
			}
			if tc.coverage && len(fake.recorded()) != 1 {
				// The lane's own results still go; only the escaping input is dropped.
				t.Fatalf("%d RecordTestRun calls, want the lane's results uploaded once", len(fake.recorded()))
			}
		})
	}
}
