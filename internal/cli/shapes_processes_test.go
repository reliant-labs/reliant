package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every process is a shape: each //forge:root struct and each workload
// literal is listed with file:line, filterable by --kind and --grep, and
// printable as JSON.
func TestCollectShapes_RootsAndWorkloads(t *testing.T) {
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"internal/app/roots.go": `package app

//forge:root ingest command=ingest migrate=none
type Ingest struct {
	Queue *Queue
}
`,
		"deploy/kcl/workloads.k": `import forge as fw

ingest = fw.Workload {
    name = "ingest"
    kind = "stream-consumer"
    image = "ghcr.io/acme/ingest"
    build = fw.DockerBuild {dockerfile = "Dockerfile"}
}
`,
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	shapes := collectShapes(dir)

	cases := []struct {
		kinds string
		grep  string
		want  []shape
	}{
		{
			kinds: "root",
			want:  []shape{{"root", "ingest", "internal/app/roots.go", 4, "type=Ingest command=ingest migrate=none fields=1"}},
		},
		{
			kinds: "workload",
			want:  []shape{{"workload", "ingest", "deploy/kcl/workloads.k", 3, "kind=stream-consumer build=docker image=ghcr.io/acme/ingest"}},
		},
		{
			grep: "stream-consumer",
			want: []shape{{"workload", "ingest", "deploy/kcl/workloads.k", 3, "kind=stream-consumer build=docker image=ghcr.io/acme/ingest"}},
		},
		{
			kinds: "root,workload",
			grep:  "^ingest$",
			want: []shape{
				{"root", "ingest", "internal/app/roots.go", 4, "type=Ingest command=ingest migrate=none fields=1"},
				{"workload", "ingest", "deploy/kcl/workloads.k", 3, "kind=stream-consumer build=docker image=ghcr.io/acme/ingest"},
			},
		},
		{kinds: "root", grep: "nothing-matches", want: []shape{}},
	}
	for _, tc := range cases {
		t.Run(tc.kinds+"/"+tc.grep, func(t *testing.T) {
			var want map[string]bool
			if tc.kinds != "" {
				want = map[string]bool{}
				for _, k := range strings.Split(tc.kinds, ",") {
					want[k] = true
				}
			}
			var pat *regexp.Regexp
			if tc.grep != "" {
				pat = regexp.MustCompile("(?i)" + tc.grep)
			}
			var buf bytes.Buffer
			if err := writeShapesJSON(&buf, shapes, want, pat); err != nil {
				t.Fatal(err)
			}
			var got []shape
			if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
				t.Fatalf("not a JSON array of shapes: %v\n%s", err, buf.String())
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d shapes, want %d:\n%s", len(got), len(tc.want), buf.String())
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("shape %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
