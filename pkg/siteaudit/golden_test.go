package siteaudit

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/*/report.golden.json from the current evaluation")

// goldenCase is testdata/<case>/case.json: an Input whose bodies are files
// beside it, so the HTML reads as HTML.
type goldenCase struct {
	Site   string `json:"site"`
	Expect struct {
		Indexing      Indexing `json:"indexing"`
		CanonicalHost string   `json:"canonical_host"`
	} `json:"expect"`
	Pages       []goldenPage    `json:"pages"`
	Robots      *goldenFetched  `json:"robots"`
	Sitemaps    []goldenFetched `json:"sitemaps"`
	LinkResults []struct {
		URL         string `json:"url"`
		Status      int    `json:"status"`
		Err         string `json:"err"`
		Placeholder bool   `json:"placeholder"`
	} `json:"link_results"`
	NotFound *goldenPage `json:"not_found"`
	// Raw runs EvaluateRaw instead of Evaluate.
	Raw bool `json:"raw"`
}

type goldenPage struct {
	URL      string      `json:"url"`
	Status   int         `json:"status"`
	Header   http.Header `json:"header"`
	Raw      string      `json:"raw"`
	Rendered string      `json:"rendered"`
	Axe      string      `json:"axe"`
	Err      string      `json:"err"`
}

type goldenFetched struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
	File   string `json:"file"`
	Err    string `json:"err"`
}

func readCaseFile(t *testing.T, dir, name string) string {
	t.Helper()
	if name == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (g goldenPage) page(t *testing.T, dir string) Page {
	p := Page{URL: g.URL, Status: g.Status, Header: g.Header, Err: g.Err,
		RawHTML: readCaseFile(t, dir, g.Raw), RenderedHTML: readCaseFile(t, dir, g.Rendered)}
	if g.Axe != "" {
		p.Axe = &AxeResult{}
		if err := json.Unmarshal([]byte(readCaseFile(t, dir, g.Axe)), p.Axe); err != nil {
			t.Fatalf("%s: %v", g.Axe, err)
		}
	}
	return p
}

func loadCase(t *testing.T, dir string) (Input, bool) {
	t.Helper()
	var c goldenCase
	if err := json.Unmarshal([]byte(readCaseFile(t, dir, "case.json")), &c); err != nil {
		t.Fatalf("case.json: %v", err)
	}
	in := Input{Site: c.Site, Expect: Expect{Indexing: c.Expect.Indexing, CanonicalHost: c.Expect.CanonicalHost}}
	for _, p := range c.Pages {
		in.Pages = append(in.Pages, p.page(t, dir))
	}
	if c.Robots != nil {
		in.Robots = &Fetched{URL: c.Robots.URL, Status: c.Robots.Status, Err: c.Robots.Err, Body: readCaseFile(t, dir, c.Robots.File)}
	}
	for _, s := range c.Sitemaps {
		in.Sitemaps = append(in.Sitemaps, Fetched{URL: s.URL, Status: s.Status, Err: s.Err, Body: readCaseFile(t, dir, s.File)})
	}
	for _, l := range c.LinkResults {
		in.LinkResults = append(in.LinkResults, LinkResult{URL: l.URL, Status: l.Status, Err: l.Err, Placeholder: l.Placeholder})
	}
	if c.NotFound != nil {
		nf := c.NotFound.page(t, dir)
		in.NotFound = &nf
	}
	return in, c.Raw
}

// TestGolden evaluates each testdata case and compares the report, as
// JSON, with its golden file. Run with -update to rewrite the goldens after
// an intended change (and bump RulesVersion if a rule, severity or weight
// moved).
func TestGolden(t *testing.T) {
	dirs, err := filepath.Glob("testdata/*/case.json")
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no golden cases: %v", err)
	}
	for _, cf := range dirs {
		dir := filepath.Dir(cf)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			in, raw := loadCase(t, dir)
			var rep Report
			if raw {
				rep = EvaluateRaw(in)
			} else {
				rep = Evaluate(in)
			}
			got, err := json.MarshalIndent(rep, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := filepath.Join(dir, "report.golden.json")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test ./pkg/siteaudit -run TestGolden -update)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("report differs from %s (rerun with -update and review the diff):\n%s", golden, got)
			}
			// The same facts always give the same report.
			again, _ := json.MarshalIndent(Evaluate(in), "", "  ")
			if !raw && !bytes.Equal(append(again, '\n'), got) {
				t.Error("Evaluate is not deterministic over the same input")
			}
		})
	}
}
