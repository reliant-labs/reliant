package vulnpatch

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/vulnscan"
)

func pyFinding(id, name, version string, fixed ...string) vulnscan.Finding {
	return vulnscan.Finding{
		ID: id, Severity: vulnscan.SeverityHigh, FixState: vulnscan.FixStateFixed, FixedIn: fixed,
		Package: vulnscan.Package{Name: name, Version: version, Type: "python", PURL: "pkg:pypi/" + strings.ToLower(name) + "@" + version},
	}
}

func TestPipRewritesHandKeptPins(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{
		"requirements.txt":     "# app deps\nrequests[socks] == 2.31.0 ; python_version >= \"3.8\"  # http\nPyYAML==5.3.1\nflask>=2.0\n",
		"requirements-dev.txt": "-r requirements.txt\npytest==8.0.0\n",
	})
	doc, fr := patch(t, root, []vulnscan.Finding{
		pyFinding("GHSA-9wx4-h78v-vm56", "requests", "2.31.0", "2.32.0"),
		pyFinding("PYSEC-2020-176", "pyyaml", "5.3.1", "5.4"), // the scan's name, PEP 503-normalized
	}, Options{}, nil, nil)
	wantCmds(t, fr)
	if doc.ExitCode != ExitOK || doc.Summary.Bumped != 2 || !doc.Changed {
		t.Fatalf("exit %d summary %+v: %+v", doc.ExitCode, doc.Summary, doc.Findings)
	}
	want := "# app deps\nrequests[socks] == 2.32.0 ; python_version >= \"3.8\"  # http\nPyYAML==5.4\nflask>=2.0\n"
	if got := readFile(t, root, "requirements.txt"); got != want {
		t.Errorf("requirements.txt =\n%s\nwant\n%s", got, want)
	}
	b := doc.Bumps[0]
	if b.Ecosystem != EcosystemPip || b.Manifest != "requirements.txt" || b.From != "2.31.0" || b.To != "2.32.0" || !b.Direct || len(b.Commands) != 0 {
		t.Errorf("bump = %+v", b)
	}
	if len(b.Edits) != 2 || b.Edits[0] != "requirements.txt: requests==2.31.0 -> requests==2.32.0" {
		t.Errorf("edits = %q", b.Edits)
	}
	if r := doc.Findings[0]; r.Ecosystem != EcosystemPip || r.Target != "2.32.0" || len(r.Manifests) != 1 {
		t.Errorf("result = %+v", r)
	}
}

func TestPipSkips(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{
		"hashed/requirements.txt": "requests==2.31.0 \\\n    --hash=sha256:aaaa\n",
		"loose/requirements.txt":  "flask>=2.0\njinja2\n",
		"svc/Pipfile.lock":        "{}",
	})
	doc, fr := patch(t, root, []vulnscan.Finding{
		pyFinding("CVE-1", "requests", "2.31.0", "2.32.0"),
		pyFinding("CVE-2", "flask", "2.2.0", "2.2.5"),
		pyFinding("CVE-3", "jinja2", "3.1.2", "3.1.3"),
		pyFinding("CVE-4", "urllib3", "1.26.0", "1.26.18"),
		pyFinding("CVE-5", "django", "3.2.0", "4.2.0"),
	}, Options{}, nil, nil)
	wantCmds(t, fr)
	for i, want := range []string{
		"hashed/requirements.txt: it pins with --hash",
		`loose/requirements.txt names flask as ">=2.0", not a pin`,
		"loose/requirements.txt names jinja2 with no version",
		"svc/Pipfile.lock has no patcher",
		"needs a major bump",
	} {
		if r := doc.Findings[i]; r.Status != StatusSkipped || !strings.Contains(r.Reason, want) {
			t.Errorf("finding %d = %s %q, want skipped mentioning %q", i, r.Status, r.Reason, want)
		}
	}
	if doc.ExitCode != ExitNothingToDo || doc.Changed {
		t.Errorf("exit %d changed %v", doc.ExitCode, doc.Changed)
	}
}

// TestPythonResolution pins B2 for Python: a finding resolves only to the
// manifests that pin exactly the scanned version, narrowed by its location;
// otherwise, or when the candidates are of several tools, it is skipped.
func TestPythonResolution(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{
		"a/requirements.txt": "requests==2.31.0\nidna==3.7\n",
		"b/requirements.txt": "requests==2.31.0\n",
		"c/requirements.txt": "requests==2.29.0\n",
	})
	doc, fr := patch(t, root, []vulnscan.Finding{
		pyFinding("CVE-1", "requests", "2.31.0", "2.32.0"),
		pyFinding("CVE-2", "idna", "3.6", "3.7"),
		pyFinding("CVE-3", "requests", "2.30.0", "2.32.0"),
	}, Options{}, nil, nil)
	wantCmds(t, fr)
	if r := doc.Findings[0]; r.Status != StatusBumped || strings.Join(r.Manifests, ",") != "a/requirements.txt,b/requirements.txt" {
		t.Errorf("exact = %+v", r)
	}
	if r := doc.Findings[1]; r.Status != StatusSkipped || r.Reason != "no Python manifest pins the scanned idna 3.6 (a/requirements.txt pins 3.7): which copy the finding is about is not known" {
		t.Errorf("no exact pin = %+v", r)
	}
	if r := doc.Findings[2]; r.Status != StatusSkipped || !strings.Contains(r.Reason, "a/requirements.txt pins 2.31.0; b/requirements.txt pins 2.31.0; c/requirements.txt pins 2.29.0") {
		t.Errorf("no exact pin, older ones = %+v", r)
	}
	if got := readFile(t, root, "c/requirements.txt"); got != "requests==2.29.0\n" {
		t.Errorf("c/requirements.txt pins another version and was changed: %q", got)
	}

	// A location from a scan of this tree narrows it.
	root = writeTree(t, map[string]string{
		"a/requirements.txt": "requests==2.31.0\n",
		"b/requirements.txt": "requests==2.31.0\n",
	})
	f := pyFinding("CVE-1", "requests", "2.31.0", "2.32.0")
	f.Locations = []string{"/b/requirements.txt"}
	doc, _ = patch(t, root, []vulnscan.Finding{f}, Options{}, nil, nil)
	if r := doc.Findings[0]; r.Status != StatusBumped || strings.Join(r.Manifests, ",") != "b/requirements.txt" {
		t.Errorf("hinted = %+v", r)
	}
	if got := readFile(t, root, "a/requirements.txt"); got != "requests==2.31.0\n" {
		t.Errorf("a/requirements.txt = %q", got)
	}

	// A requirements file beside a poetry.lock pinning the same version: which
	// tool builds the artifact is ambiguous.
	root = writeTree(t, map[string]string{
		"requirements.txt": "requests==2.31.0\n",
		"pyproject.toml":   "[tool.poetry.dependencies]\nrequests = \"^2.31\"\n",
		"poetry.lock":      poetryLock,
	})
	doc, fr = patch(t, root, []vulnscan.Finding{pyFinding("CVE-1", "requests", "2.31.0", "2.32.0")}, Options{}, nil, nil)
	wantCmds(t, fr)
	if r := doc.Findings[0]; r.Status != StatusSkipped || !strings.Contains(r.Reason, "several package managers (pip: requirements.txt; poetry: poetry.lock)") {
		t.Errorf("mixed = %+v", r)
	}
	if doc.Changed {
		t.Error("a mixed finding changed the tree")
	}
}

// TestPipRewritesOnlyWhatItReads: forge rewrites a requirements file itself
// only when it read every line, and the line-wise edit finds every pin the
// parse did; and a finding is bumped in all its manifests or in none.
func TestPipRewritesOnlyWhatItReads(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, body, want string }{
		{"malformed specifier", "requests==2.31.0\nflask 2.0\n", `it has a line forge does not read as a requirement ("flask 2.0")`},
		{"pin split across lines", "requests \\\n    ==2.31.0\n", "it pins requests==2.31.0 1 time(s), and a line-wise rewrite finds 0"},
	} {
		root := writeTree(t, map[string]string{
			"a/requirements.txt": "requests==2.31.0\n",
			"b/requirements.txt": tc.body,
		})
		doc, _ := patch(t, root, []vulnscan.Finding{pyFinding("CVE-1", "requests", "2.31.0", "2.32.0")}, Options{}, nil, nil)
		if r := doc.Findings[0]; r.Status != StatusSkipped || !strings.Contains(r.Reason, "b/requirements.txt: "+tc.want) || len(r.Manifests) != 0 {
			t.Errorf("%s: %+v", tc.name, r)
		}
		if doc.Changed || readFile(t, root, "a/requirements.txt") != "requests==2.31.0\n" || readFile(t, root, "b/requirements.txt") != tc.body {
			t.Errorf("%s: a finding one manifest cannot take changed the other", tc.name)
		}
	}

	// A local path, a URL and an option are read, not refused.
	root := writeTree(t, map[string]string{
		"requirements.txt": "--index-url https://pypi.org/simple\n./libs/shared\nhttps://example.com/x.whl\nrequests==2.31.0\n",
	})
	doc, _ := patch(t, root, []vulnscan.Finding{pyFinding("CVE-1", "requests", "2.31.0", "2.32.0")}, Options{}, nil, nil)
	if r := doc.Findings[0]; r.Status != StatusBumped {
		t.Errorf("understood file = %+v", r)
	}
}

// TestPythonVerifyIdentity: the rescan matches a Python package by its PEP
// 503 name and PEP 440 version, with or without a purl.
func TestPythonVerifyIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		found vulnscan.Package
		want  Status
	}{
		{"same copy, other spelling, no purl", vulnscan.Package{Name: "PyYAML", Version: "5.3.1", Type: "python"}, StatusStillPresent},
		{"target, PEP 440 equal", vulnscan.Package{Name: "py_yaml", Version: "5.4.0", Type: "python"}, StatusFixed},
		{"target, purl spelling", vulnscan.Package{Name: "PyYAML", Version: "5.4.0", Type: "python", PURL: "pkg:pypi/PyYAML@5.4.0"}, StatusStillPresent},
		{"another copy", vulnscan.Package{Name: "pyyaml", Version: "3.0", Type: "python"}, StatusFixed},
		{"another package", vulnscan.Package{Name: "ruamel.yaml", Version: "5.3.1", Type: "python"}, StatusFixed},
	} {
		root := writeTree(t, map[string]string{"requirements.txt": "PyYAML==5.3.1\n"})
		rescan := func(context.Context, string) ([]vulnscan.Finding, error) {
			return []vulnscan.Finding{{ID: "PYSEC-1", Package: tc.found}}, nil
		}
		doc, _ := patch(t, root, []vulnscan.Finding{pyFinding("PYSEC-1", "pyyaml", "5.3.1", "5.4")}, Options{}, nil, &VerifyOptions{Rescan: rescan})
		if r := doc.Findings[0]; r.Status != tc.want {
			t.Errorf("%s: %s %q, want %s", tc.name, r.Status, r.Reason, tc.want)
		}
	}
}

// pip-compile output, with the header pip-tools writes.
const compiledTxt = `#
# This file is autogenerated by pip-compile with Python 3.12
# by the following command:
#
#    pip-compile --generate-hashes --output-file=requirements.txt requirements.in
#
certifi==2024.2.2 \
    --hash=sha256:aaaa
    # via requests
requests==2.31.0 \
    --hash=sha256:bbbb
    # via -r requirements.in
urllib3==2.2.1 \
    --hash=sha256:cccc
    # via requests
`

func TestPipCompile(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{
		"requirements.in":  "requests==2.31.0  # pinned on purpose\n",
		"requirements.txt": compiledTxt,
	})
	doc, fr := patch(t, root, []vulnscan.Finding{
		pyFinding("CVE-1", "requests", "2.31.0", "2.32.0"),
		pyFinding("CVE-2", "urllib3", "2.2.1", "2.2.2"), // transitive: --upgrade-package alone
	}, Options{}, nil, nil)
	wantCmds(t, fr, ".: pip-compile --generate-hashes requirements.in --quiet --output-file=requirements.txt --upgrade-package requests==2.32.0 --upgrade-package urllib3==2.2.2")
	if got := readFile(t, root, "requirements.in"); got != "requests==2.32.0  # pinned on purpose\n" {
		t.Errorf("requirements.in = %q", got)
	}
	if got := readFile(t, root, "requirements.txt"); got != compiledTxt {
		t.Error("the compiled file was edited by hand; pip-compile rewrites it")
	}
	if doc.ExitCode != ExitOK || !doc.Bumps[0].Direct || doc.Bumps[1].Direct {
		t.Errorf("exit %d bumps %+v", doc.ExitCode, doc.Bumps)
	}
	if e := doc.Bumps[0].Edits; len(e) != 1 || e[0] != "requirements.in: requests==2.31.0 -> requests==2.32.0" {
		t.Errorf("edits = %q", e)
	}

	// A range in the .in that excludes the fix is a choice, not a bump.
	root = writeTree(t, map[string]string{"requirements.in": "requests<2.32\n", "requirements.txt": compiledTxt})
	doc, fr = patch(t, root, []vulnscan.Finding{pyFinding("CVE-1", "requests", "2.31.0", "2.32.0")}, Options{}, nil, nil)
	wantCmds(t, fr)
	if r := doc.Findings[0]; r.Status != StatusSkipped || !strings.Contains(r.Reason, `requirements.in constrains requests to "<2.32", which excludes the fix 2.32.0`) {
		t.Errorf("excluded = %+v", r)
	}

	// The tool's failure fails the bump, with its output.
	root = writeTree(t, map[string]string{"requirements.in": "requests\n", "requirements.txt": compiledTxt})
	doc, _ = patch(t, root, []vulnscan.Finding{pyFinding("CVE-1", "requests", "2.31.0", "2.32.0")}, Options{}, map[string]string{"pip-compile": "Could not find a version"}, nil)
	if r := doc.Findings[0]; r.Status != StatusFailed || doc.ExitCode != ExitVerify || !strings.Contains(doc.Bumps[0].Error, "Could not find a version") {
		t.Errorf("failed = %+v exit %d bumps %+v", r, doc.ExitCode, doc.Bumps)
	}
}

func TestPipCompileWithoutHeader(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{
		"api/requirements.in":  "django>=4.2\n",
		"api/requirements.txt": "django==4.2.0\n    # via -r requirements.in\nsqlparse==0.4.4\n",
	})
	doc, fr := patch(t, root, []vulnscan.Finding{pyFinding("CVE-1", "Django", "4.2.0", "4.2.11")}, Options{}, nil, nil)
	wantCmds(t, fr, "api: pip-compile --quiet --output-file=requirements.txt requirements.in --upgrade-package django==4.2.11")
	if doc.ExitCode != ExitOK || len(doc.Bumps[0].Edits) != 0 || !doc.Bumps[0].Direct {
		t.Errorf("exit %d bumps %+v", doc.ExitCode, doc.Bumps)
	}
}

// TestUVPipCompileFromRoot: the header's paths are relative to the tree's
// root, where it was run, so it reruns there; -o is kept.
func TestUVPipCompileFromRoot(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{
		"requirements/base.in": "requests\n",
		"requirements/base.txt": "# This file was autogenerated by uv via the following command:\n" +
			"#    uv pip compile requirements/base.in -o requirements/base.txt --upgrade\n" +
			"requests==2.31.0\n    # via -r requirements/base.in\n",
	})
	doc, fr := patch(t, root, []vulnscan.Finding{pyFinding("CVE-1", "requests", "2.31.0", "2.32.0")}, Options{}, nil, nil)
	wantCmds(t, fr, ".: uv pip compile requirements/base.in --quiet -o requirements/base.txt --upgrade-package requests==2.32.0")
	if doc.ExitCode != ExitOK || doc.Bumps[0].Ecosystem != EcosystemPip {
		t.Errorf("exit %d bumps %+v", doc.ExitCode, doc.Bumps)
	}

	// A header whose inputs resolve nowhere is not rerun, nor edited by hand.
	root = writeTree(t, map[string]string{
		"requirements.txt": "#    pip-compile elsewhere/reqs.in\nrequests==2.31.0\n",
	})
	doc, fr = patch(t, root, []vulnscan.Finding{pyFinding("CVE-1", "requests", "2.31.0", "2.32.0")}, Options{}, nil, nil)
	wantCmds(t, fr)
	if r := doc.Findings[0]; r.Status != StatusSkipped || !strings.Contains(r.Reason, "it is compiled, but its header records `pip-compile elsewhere/reqs.in`") {
		t.Errorf("unusable header = %+v", r)
	}
}

// TestPipCompileLayered: dev.in reads requirements.txt as a constraint, so
// requirements.txt recompiles first even though dev sorts before it.
func TestPipCompileLayered(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{
		"requirements.in":      "requests\n",
		"requirements.txt":     "requests==2.31.0\n",
		"requirements-dev.in":  "-c requirements.txt\npytest\nrequests\n",
		"requirements-dev.txt": "pytest==8.0.0\nrequests==2.31.0\n",
	})
	doc, fr := patch(t, root, []vulnscan.Finding{pyFinding("CVE-1", "requests", "2.31.0", "2.32.0")}, Options{}, nil, nil)
	wantCmds(t, fr,
		".: pip-compile --quiet --output-file=requirements.txt requirements.in --upgrade-package requests==2.32.0",
		".: pip-compile --quiet --output-file=requirements-dev.txt requirements-dev.in --upgrade-package requests==2.32.0")
	if r := doc.Findings[0]; r.Status != StatusBumped || len(r.Manifests) != 2 {
		t.Errorf("result = %+v", r)
	}
}

const poetryLock = `[[package]]
name = "requests"
version = "2.31.0"

[[package]]
name = "urllib3"
version = "2.2.1"

[[package]]
name = "Jinja2"
version = "3.1.2"

[[package]]
name = "pytest"
version = "8.0.0"

[[package]]
name = "pyjwt"
version = "2.8.0"
`

func TestPoetry(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{
		"pyproject.toml": `[tool.poetry]
name = "app"

[tool.poetry.dependencies]
python = "^3.11"
requests = "^2.31"
jinja2 = "3.1.2"

[tool.poetry.group.test.dependencies]
pytest = { version = "8.0.0", extras = ["testing"] }
pyjwt = { version = "2.8.0", markers = "sys_platform == 'linux'" }
`,
		"poetry.lock": poetryLock,
	})
	doc, fr := patch(t, root, []vulnscan.Finding{
		pyFinding("CVE-1", "requests", "2.31.0", "2.32.0"), // ^2.31 admits it: update
		pyFinding("CVE-2", "urllib3", "2.2.1", "2.2.2"),    // transitive: update
		pyFinding("CVE-3", "Jinja2", "3.1.2", "3.1.4"),     // exact: add
		pyFinding("CVE-4", "pytest", "8.0.0", "8.0.1"),     // a group, with extras
		pyFinding("CVE-5", "PyJWT", "2.8.0", "2.10.1"),     // markers poetry add would drop
	}, Options{}, nil, nil)
	wantCmds(t, fr,
		".: poetry add jinja2@^3.1.4",
		".: poetry add --group test pytest[testing]@^8.0.1",
		".: poetry update requests urllib3")
	if doc.Bumps[0].Ecosystem != EcosystemPoetry || doc.Bumps[0].Manifest != "poetry.lock" || !doc.Bumps[0].Direct || doc.Bumps[1].Direct {
		t.Errorf("bumps = %+v", doc.Bumps)
	}
	if r := doc.Findings[4]; r.Status != StatusSkipped || !strings.Contains(r.Reason, "poetry add would rewrite the declaration: it is declared with markers") {
		t.Errorf("markers = %+v", r)
	}
	if doc.ExitCode != ExitNothingToDo || doc.Summary.Bumped != 4 {
		t.Errorf("exit %d summary %+v", doc.ExitCode, doc.Summary)
	}
}

// TestPoetryMergedTarget: two advisories on one package bump it once, and
// the higher target decides between update and add.
func TestPoetryMergedTarget(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{
		"pyproject.toml": "[project]\nname = \"app\"\ndependencies = [\"requests (>=2.31,<2.32.3)\"]\n",
		"poetry.lock":    poetryLock,
	})
	doc, fr := patch(t, root, []vulnscan.Finding{
		pyFinding("CVE-1", "requests", "2.31.0", "2.32.0"),
		pyFinding("CVE-2", "requests", "2.31.0", "2.32.4"),
	}, Options{}, nil, nil)
	wantCmds(t, fr, ".: poetry add requests@^2.32.4")
	if len(doc.Bumps) != 1 || doc.Bumps[0].To != "2.32.4" || doc.Summary.Bumped != 2 {
		t.Errorf("bumps %+v summary %+v", doc.Bumps, doc.Summary)
	}
}

func TestUV(t *testing.T) {
	t.Parallel()
	pyproject := `[project]
name = "app"
dependencies = ["requests>=2.31", "jinja2==3.1.2"]

[dependency-groups]
dev = ["pytest>=8", {include-group = "lint"}]
`
	lock := strings.ReplaceAll(poetryLock, "[[package]]", "[[package]]\nsource = { registry = \"https://pypi.org/simple\" }")
	root := writeTree(t, map[string]string{"pyproject.toml": pyproject, "uv.lock": "version = 1\n" + lock})
	doc, fr := patch(t, root, []vulnscan.Finding{
		pyFinding("CVE-1", "requests", "2.31.0", "2.32.0"),
		pyFinding("CVE-2", "urllib3", "2.2.1", "2.2.2"),
		pyFinding("CVE-3", "jinja2", "3.1.2", "3.1.4"), // pinned exactly in pyproject.toml
	}, Options{}, nil, &VerifyOptions{Command: "uv run pytest -q", Source: "uv", Rescan: func(context.Context, string) ([]vulnscan.Finding, error) {
		// The rescan names the package as the lockfile does.
		return []vulnscan.Finding{pyFinding("CVE-2", "URLLib3", "2.2.1", "2.2.2")}, nil
	}})
	wantCmds(t, fr,
		".: uv lock --upgrade-package requests==2.32.0 --upgrade-package urllib3==2.2.2",
		".: sh -c uv run pytest -q")
	if r := doc.Findings[2]; r.Status != StatusSkipped || !strings.Contains(r.Reason, `uv.lock: pyproject.toml [project] dependencies constrains jinja2 to "==3.1.2", which excludes the fix 3.1.4`) {
		t.Errorf("excluded = %+v", r)
	}
	if doc.Findings[0].Status != StatusFixed || doc.Findings[1].Status != StatusStillPresent {
		t.Errorf("verified = %+v", doc.Findings[:2])
	}
	if doc.ExitCode != ExitVerify || doc.Bumps[0].Ecosystem != EcosystemUV {
		t.Errorf("exit %d bumps %+v", doc.ExitCode, doc.Bumps)
	}
}

func TestPythonEcosystemFilter(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{
		"requirements.txt":      "requests==2.31.0\n",
		"web/package.json":      `{"dependencies": {"ws": "^8.20.0"}}`,
		"web/package-lock.json": packageLock(map[string]string{"node_modules/ws": "8.20.0"}),
	})
	findings := []vulnscan.Finding{pyFinding("CVE-1", "requests", "2.31.0", "2.32.0"), npmFinding("CVE-2", "ws", "8.20.0", "8.21.0")}
	doc, fr := patch(t, root, findings, Options{Ecosystems: []Ecosystem{EcosystemUV}}, nil, nil)
	wantCmds(t, fr)
	if len(doc.Findings) != 0 {
		t.Errorf("--ecosystem uv kept %+v", doc.Findings)
	}
	doc, _ = patch(t, root, findings, Options{Ecosystems: []Ecosystem{EcosystemPip}}, nil, nil)
	if len(doc.Findings) != 1 || doc.Findings[0].Status != StatusBumped {
		t.Errorf("--ecosystem pip = %+v", doc.Findings)
	}
}

func TestPythonDefaultVerify(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		files       map[string]string
		cmd, source string
	}{
		{map[string]string{"uv.lock": "", "pyproject.toml": "[dependency-groups]\ndev = [\"pytest>=8\"]\n"}, "uv run pytest -q", "uv"},
		{map[string]string{"poetry.lock": "", "pyproject.toml": "[tool.pytest.ini_options]\naddopts = \"-q\"\n"}, "poetry run pytest -q", "poetry"},
		{map[string]string{"poetry.lock": "", "pyproject.toml": "[tool.poetry]\nname = \"x\"\n", "pytest.ini": "[pytest]\n"}, "poetry run pytest -q", "poetry"},
		{map[string]string{"uv.lock": "", "pyproject.toml": "[project]\nname = \"x\"\n"}, "", VerifySourceNone},      // no pytest
		{map[string]string{"requirements.txt": "pytest==8.0.0\n", "pytest.ini": "[pytest]\n"}, "", VerifySourceNone}, // no environment of its own
	} {
		root := writeTree(t, tc.files)
		if cmd, src := DefaultVerify(root); cmd != tc.cmd || src != tc.source {
			t.Errorf("%v: DefaultVerify = %q, %q; want %q, %q", tc.files, cmd, src, tc.cmd, tc.source)
		}
	}
}
