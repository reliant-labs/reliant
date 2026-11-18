package components

import (
	"regexp"
	"strings"
	"testing"
)

// The content-and-growth components (markdown_editor, calendar,
// upload_dropzone, time_series_chart, cohort_grid) were added together for a
// GTM app that would otherwise have hand-rolled every one. These guards pin
// the seams a caller builds against, so a refactor that drops one fails here
// rather than in a downstream app. Behaviour is proven by the vitest suites
// in testdata/behavior; types by the fixtures in testdata/typecheck.

var gtmComponents = []string{"markdown_editor", "calendar", "upload_dropzone", "time_series_chart", "cohort_grid"}

// moduleImport matches the module of every import statement.
var moduleImport = regexp.MustCompile(`(?m)^import\s[^;]*?from\s+["']([^"']+)["']`)

// TestGTMComponentsShipWithoutDependencies: a component install copies files
// and nothing else, so an npm import here is a build break in every project
// that has not installed it. Each of these is hand-rolled on purpose — the
// charts like the library's other narrative charts, the editor without a
// WYSIWYG engine.
func TestGTMComponentsShipWithoutDependencies(t *testing.T) {
	lib := NewLibrary()
	for _, name := range gtmComponents {
		src, err := lib.Get(name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		for _, m := range moduleImport.FindAllStringSubmatch(src, -1) {
			mod := m[1]
			if mod == "react" || strings.HasPrefix(mod, "./") {
				continue
			}
			t.Errorf("%s.tsx imports %q — these components ship with no dependency beyond react", name, mod)
		}
		if strings.Contains(src, "dangerouslySetInnerHTML") {
			t.Errorf("%s.tsx uses dangerouslySetInnerHTML — render user content as React elements", name)
		}
	}
}

// TestGTMComponentContracts pins each component's public seam.
func TestGTMComponentContracts(t *testing.T) {
	want := map[string][]string{
		"markdown_editor": {
			"limits?: MarkdownCharLimit[]",
			"measure?: (value: string) => number",
			"renderPreview?: (markdown: string) => React.ReactNode",
			`execCommand("insertText"`, // toolbar edits stay on the undo stack
			"Intl.Segmenter",           // limits count graphemes, as the networks do
			"controlled",
		},
		"calendar": {
			"onMove?: (itemId: string, newDate: Date) => void",
			"onDayClick?: (date: Date) => void",
			"moveKeepingTime", // a rescheduled 09:30 post stays a 09:30 post
			`role="grid"`,
			`role="gridcell"`,
			`aria-keyshortcuts={movable ? "M" : undefined}`, // the keyboard path to reschedule
			"view?: CalendarView",
			"date?: Date",
		},
		"upload_dropzone": {
			"getUploadUrl: (file: File) => Promise<T>",
			"onUploaded?: (file: File, target: T) => void",
			`xhr.open("PUT", target.url)`,
			"xhr.upload.onprogress", // fetch() reports no upload progress
			`role="progressbar"`,
			"maxBytes?: number",
			"accept?: string",
		},
		"time_series_chart": {
			"annotations?: TimeSeriesAnnotation[]",
			"value: number | null",
			"ResizeObserver",
			`role="slider"`,
			"aria-valuetext",
		},
		"cohort_grid": {
			"minN?: number",
			`dimBy?: "cohort" | "cell"`,
			"retained: Array<number | null | undefined>",
			`<caption className="sr-only">`,
			`scope="row"`,
		},
	}
	lib := NewLibrary()
	for name, needles := range want {
		src, err := lib.Get(name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		for _, needle := range needles {
			if !strings.Contains(src, needle) {
				t.Errorf("%s.tsx no longer contains %q", name, needle)
			}
		}
	}
}

// TestGTMComponentsAreFindable: the reason these exist is that a search for
// the thing came back empty. Each must be the top hit for the words an agent
// would use.
func TestGTMComponentsAreFindable(t *testing.T) {
	lib := NewLibrary()
	for query, want := range map[string]string{
		"markdown editor":   "markdown_editor",
		"content calendar":  "calendar",
		"file upload":       "upload_dropzone",
		"presigned":         "upload_dropzone",
		"time series":       "time_series_chart",
		"chart annotations": "time_series_chart",
		"cohort retention":  "cohort_grid",
	} {
		results := lib.Search(query)
		if len(results) == 0 || results[0].Name != want {
			got := "nothing"
			if len(results) > 0 {
				got = results[0].Name
			}
			t.Errorf("search %q: top hit is %s, want %s", query, got, want)
		}
	}
}
