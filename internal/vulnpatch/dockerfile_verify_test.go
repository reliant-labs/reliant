package vulnpatch

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/vulnscan"
)

// A rebuilt image settles a result only on the same advisory, the same
// package and the copy the result targeted.
func TestImageVerifyMatchesTheTargetedCopy(t *testing.T) {
	t.Parallel()
	openssl := apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6")
	for name, tc := range map[string]struct {
		rescan []vulnscan.Finding
		want   Status
	}{
		// An image holds one copy of an OS package: a refreshed base that
		// moved it, but not far enough, still ships it.
		"an OS package at another version": {rescan: []vulnscan.Finding{apkFinding("CVE-1", "openssl", "3.1.4-r5a", "3.1.4-r6")}, want: StatusStillPresent},
		"the advisory on another package":  {rescan: []vulnscan.Finding{apkFinding("CVE-1", "libcrypto3", "3.1.4-r5", "3.1.4-r6")}, want: StatusFixed},
		"another advisory on the package":  {rescan: []vulnscan.Finding{apkFinding("CVE-2", "openssl", "3.1.4-r5", "3.1.4-r7")}, want: StatusFixed},
	} {
		root := writeTree(t, map[string]string{"Dockerfile": multiStage})
		doc, _ := patch(t, root, []vulnscan.Finding{openssl}, Options{Registry: alpineRegistry()}, nil, &VerifyOptions{
			ScanImage: func(context.Context, string) ([]vulnscan.Finding, error) { return tc.rescan, nil },
		})
		if r := doc.Findings[0]; r.Status != tc.want {
			t.Errorf("%s: %s %q, want %s", name, r.Status, r.Reason, tc.want)
		}
	}

	for name, tc := range map[string]struct {
		got  string
		want Status
		note bool
	}{
		"the bump did not take":       {got: "go1.22.3", want: StatusStillPresent},
		"the fix did not fix it":      {got: "go1.22.5", want: StatusStillPresent},
		"a binary built with old Go":  {got: "go1.21.0", want: StatusFixed, note: true},
		"spelled without the go word": {got: "1.22.3", want: StatusStillPresent},
	} {
		root := writeTree(t, map[string]string{"Dockerfile": "FROM golang:1.22.3 AS b\nFROM scratch\n"})
		doc, _ := patch(t, root, []vulnscan.Finding{stdlibFinding("GO-1", "go1.22.3", "1.22.5")}, Options{Registry: golangRegistry()}, nil, &VerifyOptions{
			ScanImage: func(context.Context, string) ([]vulnscan.Finding, error) {
				return []vulnscan.Finding{stdlibFinding("GO-1", tc.got, "1.22.5")}, nil
			},
		})
		if r := doc.Findings[0]; r.Status != tc.want {
			t.Errorf("stdlib, %s: %s %q, want %s", name, r.Status, r.Reason, tc.want)
		}
		if got := strings.Contains(strings.Join(doc.Notes, "\n"), "another copy than the one bumped"); got != tc.note {
			t.Errorf("stdlib, %s: notes %v", name, doc.Notes)
		}
	}
}

// Each Dockerfile a result changed is rebuilt and reported on its own: the
// worst outcome wins, and every Dockerfile that shares it is named.
func TestImageVerifyReportsPerDockerfile(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"api/Dockerfile":    "FROM golang:1.22.3 AS b\nFROM scratch\n",
		"worker/Dockerfile": "FROM golang:1.22.3 AS b\nFROM scratch\n",
	}
	run := func(still map[string]bool) Document {
		root := writeTree(t, files)
		abs, err := filepath.Abs(root) // the tree the plan names its tags by
		if err != nil {
			t.Fatal(err)
		}
		doc, _ := patch(t, root, []vulnscan.Finding{stdlibFinding("GO-1", "go1.22.3", "1.22.5")}, Options{Registry: golangRegistry()}, nil, &VerifyOptions{
			ScanImage: func(_ context.Context, image string) ([]vulnscan.Finding, error) {
				for m := range still {
					if image == verifyTag(abs, m) {
						return []vulnscan.Finding{stdlibFinding("GO-1", "go1.22.3", "1.22.5")}, nil
					}
				}
				return nil, nil
			},
		})
		return doc
	}
	doc := run(map[string]bool{"worker/Dockerfile": true})
	if r := doc.Findings[0]; r.Status != StatusStillPresent || !strings.Contains(r.Reason, "worker/Dockerfile") || strings.Contains(r.Reason, "api/Dockerfile") {
		t.Errorf("one image still vulnerable: %s %q", r.Status, r.Reason)
	}
	if strings.Join(doc.Verify.Rebuilt, ",") != "api/Dockerfile,worker/Dockerfile" {
		t.Errorf("rebuilt %v", doc.Verify.Rebuilt)
	}
	doc = run(map[string]bool{"api/Dockerfile": true, "worker/Dockerfile": true})
	if r := doc.Findings[0]; r.Status != StatusStillPresent || !strings.Contains(r.Reason, "api/Dockerfile") || !strings.Contains(r.Reason, "worker/Dockerfile") {
		t.Errorf("both images still vulnerable: %s %q", r.Status, r.Reason)
	}
	if r := run(nil).Findings[0]; r.Status != StatusFixed || r.Reason != "" {
		t.Errorf("both clean: %s %q", r.Status, r.Reason)
	}
}

// A directory rescan judges the Go standard library by its release too,
// whether or not the version carries the "go" prefix the toolchain
// directive has.
func TestDirectoryRescanOfTheGoStandardLibrary(t *testing.T) {
	t.Parallel()
	for got, want := range map[string]Status{"go1.22.5": StatusStillPresent, "1.22.6": StatusStillPresent, "go1.22.3": StatusStillPresent, "go1.21.0": StatusFixed} {
		root := writeTree(t, map[string]string{"go.mod": goModApp})
		doc, _ := patch(t, root, []vulnscan.Finding{stdlibFinding("GO-1", "go1.22.3", "1.22.5")}, Options{Registry: golangRegistry()}, nil, &VerifyOptions{
			Rescan: func(context.Context, string) ([]vulnscan.Finding, error) {
				return []vulnscan.Finding{stdlibFinding("GO-1", got, "1.22.5")}, nil
			},
		})
		if r := doc.Findings[0]; r.Status != want {
			t.Errorf("rescan at %s: %s %q, want %s", got, r.Status, r.Reason, want)
		}
	}
}
