package vulnpatch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/testkit"
)

const golangRepo = "docker.io/library/golang"

func stdlibFinding(id, installed string, fixed ...string) vulnscan.Finding {
	return vulnscan.Finding{
		ID: id, Severity: vulnscan.SeverityHigh, FixState: vulnscan.FixStateFixed, FixedIn: fixed,
		Package: vulnscan.Package{Name: "stdlib", Version: installed, Type: "go-module", PURL: "pkg:golang/stdlib@" + installed},
	}
}

func golangRegistry() *fakeRegistry {
	return &fakeRegistry{digests: map[string]string{
		golangRepo + ":1.22.5-alpine3.19": dg("5"),
		golangRepo + ":1.22.5":            dg("6"),
		golangRepo + ":1.22-alpine":       dg("2"),
		golangRepo + ":1.23-alpine":       dg("3"),
		golangRepo + ":1.23.1":            dg("7"),
	}, tags: map[string][]string{
		golangRepo: {"1.22.3-alpine3.19", "1.22.5-alpine3.19", "1.22.6-alpine3.19", "1.22.5", "1.22-alpine", "1.23-alpine", "1.23.1"},
	}}
}

func TestChooseGoTarget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		installed  string
		fixed      []string
		allowMajor bool
		want, err  string
	}{
		{installed: "go1.22.3", fixed: []string{"1.21.11", "1.22.5", "1.23.1"}, want: "1.22.5"},
		{installed: "go1.22.3", fixed: []string{"go1.22.4", "go1.22.5"}, want: "go1.22.4"},
		{installed: "go1.22.3", fixed: []string{"1.23.1"}, err: "needs a new Go release: the lowest fix is 1.23.1"},
		{installed: "go1.22.3", fixed: []string{"1.23.1", "1.24.0"}, allowMajor: true, want: "1.23.1"},
		{installed: "go1.22.3", fixed: []string{"1.22.3rc1", "1.21.11"}, err: "no fixed Go release at or above go1.22.3"},
		{installed: "devel", fixed: []string{"1.22.5"}, err: "not a release"},
	} {
		got, err := chooseGoTarget(tc.installed, tc.fixed, tc.allowMajor)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s %v: %q, %v; want error %q", tc.installed, tc.fixed, got, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s %v: %q, %v; want %s", tc.installed, tc.fixed, got, err, tc.want)
		}
	}
	_, err := chooseGoTarget("go1.22.3", []string{"1.23.1"}, false)
	if !errors.Is(err, ErrNeedsMajor) {
		t.Errorf("a new Go release is not ErrNeedsMajor: %v", err)
	}
}

func TestGoStdlibRaisesTheToolchainAndTheBuilderImage(t *testing.T) {
	t.Parallel()
	df := "FROM golang:1.22.3-alpine3.19 AS build\nRUN go build -o /app .\nFROM alpine:3.19\nCOPY --from=build /app /app\n"
	root := writeTree(t, map[string]string{"go.mod": goModApp, "Dockerfile": df})
	reg := golangRegistry()
	doc, fr := patch(t, root, []vulnscan.Finding{
		stdlibFinding("GO-2026-1", "go1.22.3", "1.21.11", "1.22.5", "1.23.1"),
		stdlibFinding("GO-2026-2", "go1.22.3", "1.22.4"),
	}, Options{Registry: reg}, nil, nil)
	wantCmds(t, fr, ".: go mod edit -toolchain=go1.22.5")
	r := doc.Findings[0]
	if r.Status != StatusBumped || r.Ecosystem != EcosystemGo || r.Target != "1.22.5" || strings.Join(r.Manifests, ",") != "go.mod,Dockerfile" {
		t.Errorf("finding = %+v", r)
	}
	if got := readFile(t, root, "Dockerfile"); got != strings.Replace(df, "golang:1.22.3-alpine3.19", "golang:1.22.5-alpine3.19", 1) {
		t.Errorf("Dockerfile:\n%s", got)
	}
	// One toolchain bump and one image bump, both at the higher target.
	if len(doc.Bumps) != 2 {
		t.Fatalf("bumps = %+v", doc.Bumps)
	}
	if b := doc.Bumps[0]; b.Manifest != "go.mod" || b.Package != "toolchain" || b.From != "go1.22" || b.To != "go1.22.5" {
		t.Errorf("toolchain bump = %+v", b)
	}
	if b := doc.Bumps[1]; b.Manifest != "Dockerfile" || b.Package != "golang" || b.To != "golang:1.22.5-alpine3.19" || b.Ecosystem != EcosystemDocker {
		t.Errorf("image bump = %+v", b)
	}
	if doc.ExitCode != ExitOK {
		t.Errorf("exit %d: %+v notes %v", doc.ExitCode, doc.Findings, doc.Notes)
	}
}

func TestGoStdlibWithAModuleBumpInTheSameGoMod(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{"go.mod": goModApp})
	_, fr := patch(t, root, []vulnscan.Finding{
		stdlibFinding("GO-2026-1", "go1.22.3", "1.22.5"),
		goFinding("GO-2026-2", "example.com/lib", "v1.2.0", "v1.2.5"),
	}, Options{Registry: golangRegistry()}, nil, nil)
	wantCmds(t, fr, ".: go get example.com/lib@v1.2.5", ".: go mod tidy", ".: go mod edit -toolchain=go1.22.5")
}

func TestGoStdlibCases(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		files      map[string]string
		fixed      []string
		allowMajor bool
		status     Status
		reason     string
		cmds       []string
		dockerfile string // the Dockerfile after, when it changes
		note       string
	}{
		"already on a fixed toolchain": {
			files: map[string]string{"go.mod": "module x\n\ngo 1.22\n\ntoolchain go1.22.6\n", "Dockerfile": "FROM golang:1.22-alpine AS b\nFROM scratch\n"},
			fixed: []string{"1.22.5"}, status: StatusFixed, reason: "already builds with Go 1.22.5 or later",
		},
		"a go line past the fix": {
			files: map[string]string{"go.mod": "module x\n\ngo 1.23.0\n"},
			fixed: []string{"1.22.5"}, status: StatusFixed,
		},
		"go.work too": {
			files: map[string]string{"go.work": "go 1.22\n\nuse ./a\n", "a/go.mod": "module a\n\ngo 1.22.1\n"},
			fixed: []string{"1.22.5"}, status: StatusBumped,
			cmds: []string{"a: go mod edit -toolchain=go1.22.5", ".: go work edit -toolchain=go1.22.5"},
		},
		"a new Go release needs --allow-major": {
			files: map[string]string{"go.mod": goModApp},
			fixed: []string{"1.23.1"}, status: StatusSkipped, reason: "needs a new Go release",
		},
		"--allow-major moves a two-part builder tag": {
			files: map[string]string{"go.mod": goModApp, "Dockerfile": "FROM golang:1.22-alpine AS b\nFROM scratch\n"},
			fixed: []string{"1.23.1"}, allowMajor: true, status: StatusBumped,
			cmds: []string{".: go mod edit -toolchain=go1.23.1"}, dockerfile: "FROM golang:1.23-alpine AS b\nFROM scratch\n",
		},
		"a two-part builder tag floats over the fix": {
			files: map[string]string{"Dockerfile": "FROM golang:1.22-alpine AS b\nFROM scratch\n"},
			fixed: []string{"1.22.5"}, status: StatusFixed,
		},
		"a pinned two-part builder tag is re-pinned": {
			files: map[string]string{"Dockerfile": "FROM golang:1.22-alpine@" + dg("0") + " AS b\nFROM scratch\n"},
			fixed: []string{"1.22.5"}, status: StatusBumped, dockerfile: "FROM golang:1.22-alpine@" + dg("2") + " AS b\nFROM scratch\n",
		},
		"a pinned builder moves tag and digest": {
			files: map[string]string{"Dockerfile": "FROM docker.io/library/golang:1.22.3@" + dg("0") + " AS b\nFROM scratch\n"},
			fixed: []string{"1.22.5"}, status: StatusBumped, dockerfile: "FROM docker.io/library/golang:1.22.5@" + dg("6") + " AS b\nFROM scratch\n",
		},
		"an unpublished builder tag skips the whole finding": {
			// Raising go.mod alone would leave the image building with
			// the vulnerable Go: everywhere or nowhere.
			files: map[string]string{"go.mod": goModApp, "Dockerfile": "FROM golang:1.22.3-bookworm AS b\nFROM scratch\n"},
			fixed: []string{"1.22.5"}, status: StatusSkipped,
			reason: "golang:1.22.5-bookworm is not published: golang:1.22.3-bookworm left as is; the 1 other place(s) below Go 1.22.5 are left too",
		},
		"an ARG-named builder beside a go.mod skips the whole finding": {
			files: map[string]string{"go.mod": goModApp, "Dockerfile": "ARG GO_VERSION=1.22.3\nFROM golang:${GO_VERSION}-alpine AS b\nFROM scratch\n"},
			fixed: []string{"1.22.5"}, status: StatusSkipped, reason: "a Go version named by a build argument is left to the agent; the 1 other place(s)",
		},
		"an ARG-named builder alone is the agent's": {
			files: map[string]string{"Dockerfile": "ARG GO_VERSION=1.22.3\nFROM golang:${GO_VERSION}-alpine AS b\nFROM scratch\n"},
			fixed: []string{"1.22.5"}, status: StatusSkipped, reason: "Dockerfile:2 builds FROM golang:${GO_VERSION}-alpine: a Go version named by a build argument is left to the agent",
		},
		"a builder for $BUILDPLATFORM still moves its tag": {
			// The platform picks an architecture of the tag, and every one
			// carries the tag's Go release.
			files: map[string]string{"Dockerfile": "FROM --platform=$BUILDPLATFORM golang:1.22.3-alpine3.19 AS b\nFROM scratch\n"},
			fixed: []string{"1.22.5"}, status: StatusBumped, dockerfile: "FROM --platform=$BUILDPLATFORM golang:1.22.5-alpine3.19 AS b\nFROM scratch\n",
		},
		"an untagged builder floats over the fix": {
			files: map[string]string{"go.mod": goModApp, "Dockerfile": "FROM golang AS b\nFROM scratch\n"},
			fixed: []string{"1.22.5"}, status: StatusBumped, cmds: []string{".: go mod edit -toolchain=go1.22.5"},
		},
		"an untagged builder pinned by digest is left": {
			files: map[string]string{"Dockerfile": "FROM golang@" + dg("0") + " AS b\nFROM scratch\n"},
			fixed: []string{"1.22.5"}, status: StatusSkipped, reason: "names no Go release in its tag, and its digest pins one",
		},
		"nothing to raise": {
			files: map[string]string{"package.json": "{}"},
			fixed: []string{"1.22.5"}, status: StatusSkipped, reason: "no go.mod, go.work or golang builder image",
		},
	} {
		root := writeTree(t, tc.files)
		doc, fr := patch(t, root, []vulnscan.Finding{stdlibFinding("GO-1", "go1.22.3", tc.fixed...)}, Options{Registry: golangRegistry(), AllowMajor: tc.allowMajor}, nil, nil)
		r := doc.Findings[0]
		if r.Status != tc.status || !strings.Contains(r.Reason, tc.reason) {
			t.Errorf("%s: %s %q, want %s %q", name, r.Status, r.Reason, tc.status, tc.reason)
		}
		if strings.Join(fr.cmds, "|") != strings.Join(tc.cmds, "|") {
			t.Errorf("%s: commands %v, want %v", name, fr.cmds, tc.cmds)
		}
		if tc.dockerfile != "" {
			if got := readFile(t, root, "Dockerfile"); got != tc.dockerfile {
				t.Errorf("%s: Dockerfile\n%s\nwant\n%s", name, got, tc.dockerfile)
			}
		} else if df, ok := tc.files["Dockerfile"]; ok && readFile(t, root, "Dockerfile") != df {
			t.Errorf("%s: Dockerfile changed:\n%s", name, readFile(t, root, "Dockerfile"))
		}
		if tc.note != "" && !strings.Contains(strings.Join(doc.Notes, "\n"), tc.note) {
			t.Errorf("%s: notes %v, want %q", name, doc.Notes, tc.note)
		}
	}
}

func TestGoStdlibVerifiesThroughTheImageWhenOneIsRebuilt(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{"go.mod": goModApp, "Dockerfile": "FROM golang:1.22.3 AS b\nFROM scratch\n"})
	var dirScans int
	doc, _ := patch(t, root, []vulnscan.Finding{stdlibFinding("GO-1", "go1.22.3", "1.22.5")}, Options{Registry: golangRegistry()}, nil, &VerifyOptions{
		Rescan: func(context.Context, string) ([]vulnscan.Finding, error) {
			dirScans++
			return []vulnscan.Finding{stdlibFinding("GO-1", "go1.22.3", "1.22.5")}, nil // a directory scan cannot see the image
		},
		ScanImage: func(context.Context, string) ([]vulnscan.Finding, error) { return nil, nil },
	})
	if r := doc.Findings[0]; r.Status != StatusFixed || dirScans != 1 || doc.ExitCode != ExitOK {
		t.Errorf("%+v dir scans %d exit %d", r, dirScans, doc.ExitCode)
	}
	// Without a Dockerfile, the directory rescan settles it.
	root = writeTree(t, map[string]string{"go.mod": goModApp})
	doc, _ = patch(t, root, []vulnscan.Finding{stdlibFinding("GO-1", "go1.22.3", "1.22.5")}, Options{Registry: golangRegistry()}, nil, &VerifyOptions{
		Rescan: func(context.Context, string) ([]vulnscan.Finding, error) {
			return []vulnscan.Finding{stdlibFinding("GO-1", "go1.22.3", "1.22.5")}, nil
		},
	})
	if r := doc.Findings[0]; r.Status != StatusStillPresent {
		t.Errorf("dir rescan: %+v", r)
	}
}

// TestRealToolchainEdit runs the toolchain bump with the real go command:
// go.mod gains the directive and keeps its go line.
func TestRealToolchainEdit(t *testing.T) {
	testkit.Slow(t, "execs go mod edit")
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := writeTree(t, map[string]string{"go.mod": "module example.com/app\n\ngo 1.20\n"})
	p, err := NewPlan(root, []vulnscan.Finding{stdlibFinding("GO-1", "go1.22.3", "1.22.5")}, Options{Registry: golangRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, c vulnscan.Command) error {
		c.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=")
		return vulnscan.Exec(ctx, c)
	}
	if err := p.Apply(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if doc := p.Document("", "", false); doc.ExitCode != ExitOK {
		t.Fatalf("exit %d: %+v", doc.ExitCode, doc.Bumps)
	}
	if got := readFile(t, root, "go.mod"); got != "module example.com/app\n\ngo 1.20\n\ntoolchain go1.22.5\n" {
		t.Errorf("go.mod:\n%s", got)
	}
}
