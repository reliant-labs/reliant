package testflakes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testreport"
)

func TestParseQuarantineTarget(t *testing.T) {
	cases := []struct {
		in, pkg, name, err string
	}{
		{in: "go:example.com/m/internal/billing:TestRefund", pkg: "example.com/m/internal/billing", name: "TestRefund"},
		{in: "go:example.com/m/internal/billing:TestRefund/partial", pkg: "example.com/m/internal/billing", name: "TestRefund"},
		{in: "./internal/billing:TestRefund", pkg: "example.com/m/internal/billing", name: "TestRefund"},
		{in: "internal/billing:TestRefund", pkg: "example.com/m/internal/billing", name: "TestRefund"},
		{in: ".:TestRoot", pkg: "example.com/m", name: "TestRoot"},
		{in: "example.com/m/x:TestX", pkg: "example.com/m/x", name: "TestX"},
		{in: "ts:web/a.test.ts:renders", err: "only Go tests"},
		{in: "internal/billing:helper", err: "not a Go test function"},
		{in: "github.com/other/mod/x:TestX", err: "not in module"},
		{in: "../escape:TestX", err: "outside the module"},
		{in: "TestNoPackage", err: "want go:"},
		// An import path under the module is not taken as it is: its
		// directory must stay inside the module (and so the entry's file
		// inside ci/quarantine/go/).
		{in: "go:example.com/m/pkg/../../../outside:TestX", err: "not a directory inside the module"},
		{in: "example.com/m/./x:TestX", err: "not a directory inside the module"},
		{in: "example.com/m//x:TestX", err: "not a directory inside the module"},
		{in: `./internal\x:TestX`, err: "not a directory inside the module"},
	}
	for _, c := range cases {
		id, err := ParseQuarantineTarget(c.in, testModule)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err = %v, want %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if id.Lang != testreport.LangGo || id.Package != c.pkg || id.Name != c.name {
			t.Errorf("%s: got %+v", c.in, id)
		}
	}
}

// EntryPath never names a file outside ci/quarantine/go/, whatever id it is
// handed: QuarantineAdd and QuarantineRemove join it to the project root.
func TestEntryPathStaysInTheQuarantineDir(t *testing.T) {
	bad := []testreport.TestID{
		{Lang: testreport.LangGo, Package: testModule + "/../../outside", Name: "TestX"},
		{Lang: testreport.LangGo, Package: testModule + "/a/../../b", Name: "TestX"},
		{Lang: testreport.LangGo, Package: testModule + "/x", Name: "TestX/../../../../outside"},
		{Lang: testreport.LangGo, Package: testModule + "/x", Name: ".."},
		{Lang: testreport.LangGo, Package: testModule + "/x", Name: `TestX\..\y`},
		{Lang: testreport.Lang("../../x"), Package: testModule + "/x", Name: "TestX"},
	}
	for _, id := range bad {
		if p, err := EntryPath(id, testModule); err == nil {
			t.Errorf("%+v: path %q, want refused", id, p)
		}
	}
	root := newModule(t, nil)
	e := Entry{Lang: testreport.LangGo, Package: testModule + "/../../outside", Test: "TestX"}
	if _, err := QuarantineAdd(root, testModule, e, false); err == nil {
		t.Fatal("QuarantineAdd wrote an entry outside ci/quarantine")
	}
	p, err := EntryPath(testreport.TestID{Lang: testreport.LangGo, Package: testModule + "/internal/a", Name: "TestA"}, testModule)
	if err != nil || p != "ci/quarantine/go/internal/a/TestA.yaml" {
		t.Errorf("path = %q, %v", p, err)
	}
}

// The file layout is control-plane's: ci/quarantine/go/<dir>/<Test>.yaml,
// _root for the root package, flat key: value lines a sed reader takes.
func TestQuarantineAddListRemove(t *testing.T) {
	root := newModule(t, nil)
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	id := testreport.TestID{Lang: testreport.LangGo, Package: testModule + "/internal/alerting", Name: "TestRuleFires"}
	e := Entry{Lang: id.Lang, Package: id.Package, Test: id.Name, Issue: "https://github.com/acme/m/issues/1", Reason: "times out: under -race", Signature: "abc123", QuarantinedAt: now, ExpiresAt: now.Add(DefaultQuarantineTTL), Commit: "0123456789ab"}
	res, err := QuarantineAdd(root, testModule, e, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Existed || res.Entry.Path != "ci/quarantine/go/internal/alerting/TestRuleFires.yaml" {
		t.Fatalf("add: %+v", res)
	}
	data, _ := os.ReadFile(filepath.Join(root, res.Entry.Path))
	for _, want := range []string{
		"lang: go\n", "package: example.com/m/internal/alerting\n", "test: TestRuleFires\n",
		"issue: https://github.com/acme/m/issues/1\n", `reason: "times out: under -race"` + "\n",
		"quarantined_at: 2026-11-21T10:00:00Z\n", "expires_at: 2026-11-23T10:00:00Z\n", "commit: 0123456789ab\n",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("entry file lacks %q:\n%s", want, data)
		}
	}

	// A second add leaves the entry alone; --force rewrites it.
	e2 := e
	e2.Issue = "https://github.com/acme/m/issues/2"
	if res, _ := QuarantineAdd(root, testModule, e2, false); !res.Existed || res.Entry.Issue != e.Issue {
		t.Errorf("re-add without force: %+v", res)
	}
	if res, _ := QuarantineAdd(root, testModule, e2, true); !res.Rewritten {
		t.Errorf("re-add with force: %+v", res)
	}

	rootEntry := Entry{Lang: testreport.LangGo, Package: testModule, Test: "TestTop"}
	if res, err := QuarantineAdd(root, testModule, rootEntry, false); err != nil || res.Entry.Path != "ci/quarantine/go/_root/TestTop.yaml" {
		t.Fatalf("root package: %+v %v", res, err)
	}

	list, errs := QuarantineList(root)
	if len(errs) > 0 || len(list) != 2 {
		t.Fatalf("list: %+v %v", list, errs)
	}
	got := list[1]
	if got.ID() != id || got.Issue != e2.Issue || got.Reason != e.Reason || !got.ExpiresAt.Equal(e.ExpiresAt) {
		t.Errorf("listed entry = %+v", got)
	}
	if got.Overdue(now) || !got.Overdue(now.Add(49*time.Hour)) {
		t.Error("overdue must turn on at expires_at")
	}
	if doc := got.Document(now.Add(72 * time.Hour)); !doc.Overdue || doc.ExpiresAt == nil {
		t.Errorf("document = %+v", doc)
	}
	if list[0].Overdue(now.Add(1000 * time.Hour)) {
		t.Error("an entry with no expires_at is never overdue")
	}

	if re := GoSkipRegex(root, ""); re != "^(TestRuleFires|TestTop)$" {
		t.Errorf("skip regex = %q", re)
	}

	if _, err := QuarantineRemove(root, testModule, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "ci/quarantine/go/internal")); !os.IsNotExist(err) {
		t.Errorf("empty package directories must go with the entry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "ci/quarantine/go")); err != nil {
		t.Errorf("the lang directory stays: %v", err)
	}
	if _, err := QuarantineRemove(root, testModule, id); err == nil || !strings.Contains(err.Error(), "not quarantined") {
		t.Errorf("second remove: %v", err)
	}
}

// Ported from quarantine.sh: names come from file names alone (non-test
// names ignored), and the CI_QUARANTINE_EXTRA overlay adds to them.
func TestGoSkipRegexReadsFileNamesAndOverlay(t *testing.T) {
	root := newModule(t, map[string]string{
		"ci/quarantine/go/internal/x/TestA.yaml": "garbage that is not yaml: [\n",
		"ci/quarantine/go/e2e/deploy/TestB.yaml": "",
		"ci/quarantine/go/internal/x/notes.yaml": "",
		"ci/quarantine/README.md":                "# readme\n",
	})
	if re := GoSkipRegex(root, ""); re != "^(TestA|TestB)$" {
		t.Errorf("skip regex = %q", re)
	}
	extra := filepath.Join(t.TempDir(), "extra.tsv")
	write(t, extra, "example.com/m/y\tTestC\nbad line\nexample.com/m/y\tnotatest\n")
	if re := GoSkipRegex(root, extra); re != "^(TestA|TestB|TestC)$" {
		t.Errorf("with overlay = %q", re)
	}
	if re := GoSkipRegex(t.TempDir(), ""); re != "" {
		t.Errorf("no list = %q, want empty", re)
	}
}

// control-plane's checked-in entries read as they are.
func TestQuarantineListReadsControlPlaneEntries(t *testing.T) {
	root := newModule(t, map[string]string{
		"ci/quarantine/go/internal/alerting/TestProdAlertNotificationPolicyReachesReleaseShape.yaml": `# Quarantined by scripts/flake-quarantine.sh (docs/flake-quarantine.md). Delete this file in the
# pull request that deletes the test or rewrites it as a deterministic test.
lang: go
package: github.com/reliant-labs/control-plane/internal/alerting
test: TestProdAlertNotificationPolicyReachesReleaseShape
issue: https://github.com/reliant-labs/control-plane/issues/3272
quarantined_at: 2026-11-21T00:29:45Z
expires_at: 2026-11-23T00:29:45Z
commit: 66217cf7ff68
run: the ci:full record /home/x/.git/ci-full/66217cf7ff6842cdd509b5c7856f78e1e12d4d4e.md
`,
	})
	list, errs := QuarantineList(root)
	if len(errs) > 0 || len(list) != 1 {
		t.Fatalf("%+v %v", list, errs)
	}
	e := list[0]
	if e.Package != "github.com/reliant-labs/control-plane/internal/alerting" || e.Issue != "https://github.com/reliant-labs/control-plane/issues/3272" ||
		e.ExpiresAt.Format(time.RFC3339) != "2026-11-23T00:29:45Z" || !strings.HasPrefix(e.Run, "the ci:full record") || e.Commit != "66217cf7ff68" {
		t.Errorf("entry = %+v", e)
	}
}
