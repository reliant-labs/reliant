package testlocal

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/testreport"
)

// The exports render a merged coverage document in formats other tools read.
// Every export counts what the report counts: excluded files (generated,
// filtered) are left out, and a line's count is the sum over lanes.

// LineHits is one file's executable lines and their counts. A line touched
// by several blocks is covered only if every one of them is (the rule diff
// coverage uses), and its count is the smallest of theirs.
func LineHits(f testreport.CoverageFile) map[int]int64 {
	out := map[int]int64{}
	for _, b := range f.Blocks {
		var sum int64
		for _, h := range b.Hits {
			if h > 0 {
				sum += h
			}
		}
		lo, hi := max(b.StartLine, 1), min(b.EndLine, b.StartLine+100_000)
		for l := lo; l <= hi; l++ {
			if have, ok := out[l]; !ok || sum < have {
				out[l] = sum
			}
		}
	}
	return out
}

func sortedLines(m map[int]int64) []int {
	ls := make([]int, 0, len(m))
	for l := range m {
		ls = append(ls, l)
	}
	sort.Ints(ls)
	return ls
}

func countedFiles(c *testreport.Coverage) []testreport.CoverageFile {
	var out []testreport.CoverageFile
	for _, f := range c.Files {
		if f.Excluded == "" {
			out = append(out, f)
		}
	}
	return out
}

// WriteLCOV writes the coverage as an LCOV tracefile, one record per file,
// named by repo-relative path (the import path when none is known).
func WriteLCOV(w io.Writer, c *testreport.Coverage) error {
	bw := bufio.NewWriter(w)
	for _, f := range countedFiles(c) {
		hits := LineHits(f)
		fmt.Fprintf(bw, "TN:\nSF:%s\n", f.DisplayPath())
		lh := 0
		for _, l := range sortedLines(hits) {
			fmt.Fprintf(bw, "DA:%d,%d\n", l, hits[l])
			if hits[l] > 0 {
				lh++
			}
		}
		fmt.Fprintf(bw, "LF:%d\nLH:%d\nend_of_record\n", len(hits), lh)
	}
	return bw.Flush()
}

type coberturaDoc struct {
	XMLName      xml.Name           `xml:"coverage"`
	LineRate     string             `xml:"line-rate,attr"`
	BranchRate   string             `xml:"branch-rate,attr"`
	LinesValid   int                `xml:"lines-valid,attr"`
	LinesCovered int                `xml:"lines-covered,attr"`
	Version      string             `xml:"version,attr"`
	Timestamp    int64              `xml:"timestamp,attr"`
	Sources      []string           `xml:"sources>source"`
	Packages     []coberturaPackage `xml:"packages>package"`
}

type coberturaPackage struct {
	Name       string           `xml:"name,attr"`
	LineRate   string           `xml:"line-rate,attr"`
	BranchRate string           `xml:"branch-rate,attr"`
	Classes    []coberturaClass `xml:"classes>class"`
}

type coberturaClass struct {
	Name       string          `xml:"name,attr"`
	Filename   string          `xml:"filename,attr"`
	LineRate   string          `xml:"line-rate,attr"`
	BranchRate string          `xml:"branch-rate,attr"`
	Lines      []coberturaLine `xml:"lines>line"`
}

type coberturaLine struct {
	Number int   `xml:"number,attr"`
	Hits   int64 `xml:"hits,attr"`
}

func rate(covered, total int) string {
	if total == 0 {
		return "0"
	}
	return fmt.Sprintf("%.4f", float64(covered)/float64(total))
}

// WriteCobertura writes the coverage as Cobertura XML: one package per
// directory, one class per file. Paths are relative to source, the
// repository root.
func WriteCobertura(w io.Writer, c *testreport.Coverage, source string, now time.Time) error {
	if source == "" {
		source = "."
	}
	doc := coberturaDoc{BranchRate: "0", Version: "forge", Timestamp: now.UnixMilli(), Sources: []string{source}}
	pkgs := map[string]*coberturaPackage{}
	pkgTally := map[string][2]int{}
	var order []string
	total, covered := 0, 0
	for _, f := range countedFiles(c) {
		p := f.DisplayPath()
		dir := path.Dir(p)
		pk := pkgs[dir]
		if pk == nil {
			pk = &coberturaPackage{Name: dir, BranchRate: "0"}
			pkgs[dir] = pk
			order = append(order, dir)
		}
		hits := LineHits(f)
		cls := coberturaClass{Name: path.Base(p), Filename: p, BranchRate: "0"}
		fc := 0
		for _, l := range sortedLines(hits) {
			cls.Lines = append(cls.Lines, coberturaLine{Number: l, Hits: hits[l]})
			if hits[l] > 0 {
				fc++
			}
		}
		cls.LineRate = rate(fc, len(hits))
		pk.Classes = append(pk.Classes, cls)
		t := pkgTally[dir]
		pkgTally[dir] = [2]int{t[0] + fc, t[1] + len(hits)}
		total += len(hits)
		covered += fc
	}
	sort.Strings(order)
	for _, d := range order {
		pk := pkgs[d]
		t := pkgTally[d]
		pk.LineRate = rate(t[0], t[1])
		doc.Packages = append(doc.Packages, *pk)
	}
	doc.LineRate, doc.LinesValid, doc.LinesCovered = rate(covered, total), total, covered
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// WriteHTML writes a self-contained HTML page: the summary, then each
// counted file. A file is drawn over its source only when the file on disk
// under repoRoot is the exact blob the counts were taken on; otherwise it is
// shown as line numbers and counts, with a line saying why, because
// highlights over different source are silently wrong.
func WriteHTML(w io.Writer, c *testreport.Coverage, repoRoot, title string) error {
	bw := bufio.NewWriter(w)
	s := c.Summary()
	fmt.Fprintf(bw, `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>%s</title>
<style>
body{font:14px/1.4 system-ui,sans-serif;margin:2rem;color:#1f2328}
table{border-collapse:collapse}td,th{padding:2px 8px;text-align:left}
th{border-bottom:1px solid #d0d7de}.num{text-align:right;font-variant-numeric:tabular-nums}
pre{margin:0;font:12px/1.5 ui-monospace,monospace}
.src td{padding:0 6px;white-space:pre}.ln{color:#6e7781;text-align:right;user-select:none}
.hit{background:#dafbe1}.miss{background:#ffebe9}.note{color:#6e7781}
h2{margin-top:2rem;font-size:15px}
</style></head><body>
<h1>%s</h1>
<p>Combined: <b>%.1f%%</b> of %d statements`, html.EscapeString(title), html.EscapeString(title), s.Combined.Pct, s.Combined.Statements)
	if s.CombinedLabel != "" {
		fmt.Fprintf(bw, " (%s)", html.EscapeString(s.CombinedLabel))
	}
	fmt.Fprint(bw, "</p>\n<table><tr><th>Lane</th><th class=num>Covered</th></tr>\n")
	lanes := make([]string, 0, len(s.Lanes))
	for l := range s.Lanes {
		lanes = append(lanes, string(l))
	}
	sort.Strings(lanes)
	for _, l := range lanes {
		t := s.Lanes[testreport.Lane(l)]
		fmt.Fprintf(bw, "<tr><td>%s</td><td class=num>%.1f%%</td></tr>\n", html.EscapeString(l), t.Pct)
	}
	fmt.Fprint(bw, "</table>\n")
	if s.Generated.Statements > 0 {
		fmt.Fprintf(bw, "<p class=note>Generated code, not counted: %d statements.</p>\n", s.Generated.Statements)
	}
	files := countedFiles(c)
	fmt.Fprint(bw, "<h2>Files</h2><table><tr><th>File</th><th class=num>Covered</th></tr>\n")
	for _, r := range c.Rollup(testreport.ByFile) {
		fmt.Fprintf(bw, "<tr><td><a href=\"#%s\">%s</a></td><td class=num>%.1f%%</td></tr>\n", anchor(r.Path), html.EscapeString(r.Path), r.Combined.Pct)
	}
	fmt.Fprint(bw, "</table>\n")
	for _, f := range files {
		writeHTMLFile(bw, f, repoRoot)
	}
	fmt.Fprint(bw, "</body></html>\n")
	return bw.Flush()
}

func anchor(p string) string {
	return "f-" + strings.NewReplacer("/", "-", ".", "-", " ", "-").Replace(p)
}

func writeHTMLFile(w io.Writer, f testreport.CoverageFile, repoRoot string) {
	p := f.DisplayPath()
	hits := LineHits(f)
	fmt.Fprintf(w, "<h2 id=\"%s\">%s</h2>\n", anchor(p), html.EscapeString(p))
	src, why := sourceFor(f, repoRoot)
	if src == nil {
		fmt.Fprintf(w, "<p class=note>Source not shown: %s. Lines and counts only.</p>\n<table class=src>", html.EscapeString(why))
		for _, l := range sortedLines(hits) {
			cls := "miss"
			if hits[l] > 0 {
				cls = "hit"
			}
			fmt.Fprintf(w, "<tr class=%s><td class=ln>%d</td><td class=num>%d</td></tr>\n", cls, l, hits[l])
		}
		fmt.Fprint(w, "</table>\n")
		return
	}
	fmt.Fprint(w, "<table class=src>")
	for i, line := range strings.Split(strings.TrimSuffix(string(src), "\n"), "\n") {
		n := i + 1
		cls, count := "", ""
		if h, ok := hits[n]; ok {
			count = fmt.Sprint(h)
			cls = " class=miss"
			if h > 0 {
				cls = " class=hit"
			}
		}
		fmt.Fprintf(w, "<tr%s><td class=ln>%d</td><td class=num>%s</td><td><pre>%s</pre></td></tr>\n", cls, n, count, html.EscapeString(line))
	}
	fmt.Fprint(w, "</table>\n")
}

// sourceFor reads a file's source when it is provably the source the counts
// describe, else says why not.
func sourceFor(f testreport.CoverageFile, repoRoot string) ([]byte, string) {
	switch {
	case repoRoot == "" || f.Path == "":
		return nil, "the file's repository path is unknown"
	case f.BlobID == "":
		return nil, "the run's source version is unknown"
	}
	b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(f.Path)))
	if err != nil {
		return nil, "the file is not in this checkout"
	}
	if testreport.GitBlobID(b) != f.BlobID {
		return nil, "the file has changed since the run"
	}
	return b, ""
}
