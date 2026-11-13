package shellbuildtokens

import "testing"

// The real control-plane shape: a raw string, which is the ONLY way a token
// survives KCL, carrying the silent one. This is the case the rule exists for.
func TestScanSourceFlagsRawStringToken(t *testing.T) {
	src := `import forge
import forge.workloads as fw

_reliant = fw.Workload {
    name = "reliant"
    build = forge.ShellBuild {
        cmd = r"""
        cd ../reliant && GOARCH=${TARGETARCH} go build -o bin/reliant ./cmd/reliant
        docker build -t ${REGISTRY}/${IMAGE}:${TAG} .
        """
        cwd = "."
    }
    runtime = forge.BuildOnly {}
}
`
	got := ScanSource("deploy/kcl/e2e/main.k", src)
	if len(got) != 4 {
		t.Fatalf("expected 4 findings (IMAGE REGISTRY TAG TARGETARCH), got %d: %+v", len(got), got)
	}
	for _, f := range got {
		if f.Workload != "reliant" {
			t.Errorf("workload = %q, want reliant (read from the enclosing literal)", f.Workload)
		}
		if f.File != "deploy/kcl/e2e/main.k" {
			t.Errorf("file = %q", f.File)
		}
		if f.Line != 6 {
			t.Errorf("line = %d, want 6 (the ShellBuild literal)", f.Line)
		}
	}
}

// The shape real projects actually use, and the one a ShellBuild-literal-only
// scan misses entirely: the command is bound to a VARIABLE (often shared across
// several workloads and several envs) and the ShellBuild just references it.
//
// This is not an edge case — it is where the dangerous token lives. Every
// `GOARCH=${TARGETARCH}` in control-plane's deploy/kcl is written this way, so a
// scanner that only reads inside `ShellBuild { ... }` braces would report the
// harmless findings and miss the one that silently builds for the wrong arch.
func TestScanSourceFlagsCmdBoundToAVariable(t *testing.T) {
	src := `_reliant_image_build_cmd = r"""
set -e
GOOS=linux GOARCH=${TARGETARCH} CGO_ENABLED=0 go build -o bin/reliant ./cmd/reliant
docker build --platform=linux/${TARGETARCH} -t ${REGISTRY}/${IMAGE}:e2e .
"""

_w = fw.Workload {
    name = "reliant-api-server"
    build = forge.ShellBuild {cmd = _reliant_image_build_cmd, cwd = "../reliant"}
}
`
	got := ScanSource("e2e/main.k", src)
	tokens := map[string]bool{}
	for _, f := range got {
		tokens[f.Token] = true
	}
	for _, want := range []string{"TARGETARCH", "REGISTRY", "IMAGE"} {
		if !tokens[want] {
			t.Errorf("a cmd bound to a variable must still be scanned; ${%s} was not reported (got %+v)", want, got)
		}
	}
	// The finding must point at the string, which is where the edit happens.
	for _, f := range got {
		if f.Line < 1 || f.Line > 5 {
			t.Errorf("finding %s points at line %d, want the command string near the top", f.Token, f.Line)
		}
	}
}

// The last two real shapes, both taken from control-plane's deploy/kcl: a
// lambda that RETURNS the command, and a command handed to a config struct
// field in another file (`reliant_api_build_cmd = _reliant_image_build_cmd`)
// which a shared helper later turns into the ShellBuild.
//
// Neither has a `ShellBuild {` anywhere near it, so both are judged by the
// project's own naming: an identifier whose name says `cmd` is a command. That
// is what makes the dangerous `GOARCH=${TARGETARCH}` reachable — it lives in
// exactly these declarations and in no inline ShellBuild at all.
func TestScanSourceFlagsCmdNamedDeclarations(t *testing.T) {
	for name, src := range map[string]string{
		"lambda returning the command": `reliant_image_build_cmd = lambda -> str {
    r"""
set -e
GOOS=linux GOARCH=${TARGETARCH} CGO_ENABLED=0 go build -o bin/reliant ./cmd/reliant
"""
}
`,
		"command passed to a config field in another file": `_reliant_image_build_cmd = r"""
set -e
GOOS=linux GOARCH=${TARGETARCH} CGO_ENABLED=0 go build -o bin/reliant ./cmd/reliant
"""

_stack = lib.Stack {reliant_api_build_cmd = _reliant_image_build_cmd}
`,
	} {
		got := ScanSource("main.k", src)
		found := false
		for _, f := range got {
			if f.Token == "TARGETARCH" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: ${TARGETARCH} must be reported — this is the silent-GOARCH shape; got %+v", name, got)
		}
	}
}

// A raw string that is NOT a build command must not be scanned — a scanner that
// read every raw string in a file would flag SQL, a Dockerfile heredoc or a
// docstring example and train people to ignore it.
func TestScanSourceIgnoresRawStringsThatAreNotCommands(t *testing.T) {
	src := `_docs = r"""
Forge used to substitute ${IMAGE} and ${TAG} into a cmd. It no longer does.
"""
_sql = r"""SELECT '${TAG}' FROM t"""
`
	if got := ScanSource("notes.k", src); len(got) != 0 {
		t.Errorf("only build commands are scanned — want no findings, got %+v", got)
	}
}

// A build that declares the token in its env map is correct and must pass —
// this is the migration path the remediation text offers, so the scanner has
// to honour it too, not just Check.
func TestScanSourceHonoursDeclaredEnv(t *testing.T) {
	src := `_w = fw.Workload {
    name = "reliant"
    build = forge.ShellBuild {
        cmd = r"GOARCH=${TARGETARCH} go build ./cmd/reliant"
        env = {"TARGETARCH" = forge.target_arch()}
    }
}
`
	if got := ScanSource("main.k", src); len(got) != 0 {
		t.Errorf("TARGETARCH is declared in env — want no findings, got %+v", got)
	}
}

// A ShellBuild written the NEW way — KCL interpolation of the accessors — must
// produce nothing. This is the shape the docs teach, so a false positive here
// would flag every correctly migrated project.
func TestScanSourcePassesPlainKCLShellBuild(t *testing.T) {
	src := `_w = fw.Workload {
    name = "reliant"
    build = forge.ShellBuild {
        cmd = "GOARCH=${_arch} go build -o bin/reliant ./cmd/reliant && docker build -t ${_ref} ."
        cwd = "../reliant"
    }
}
`
	if got := ScanSource("main.k", src); len(got) != 0 {
		t.Errorf("KCL interpolation of local values is the correct shape — want no findings, got %+v", got)
	}
}

// Several builds in one file are each judged on their own, and each names its
// own workload — a file-level scan that attributed a finding to the wrong
// workload would send the user to the wrong declaration.
func TestScanSourceAttributesPerWorkload(t *testing.T) {
	src := `_a = fw.Workload {
    name = "alpha"
    build = forge.ShellBuild {cmd = r"echo ${ENV}"}
}
_b = fw.Workload {
    name = "beta"
    build = forge.ShellBuild {cmd = "echo fine"}
}
_c = fw.Workload {
    name = "gamma"
    build = forge.ShellBuild {cmd = r"echo ${SERVICE}"}
}
`
	got := ScanSource("main.k", src)
	if len(got) != 2 {
		t.Fatalf("expected 2 findings, got %d: %+v", len(got), got)
	}
	if got[0].Workload != "alpha" || got[0].Token != "ENV" {
		t.Errorf("first finding = %+v, want alpha/ENV", got[0])
	}
	if got[1].Workload != "gamma" || got[1].Token != "SERVICE" {
		t.Errorf("second finding = %+v, want gamma/SERVICE", got[1])
	}
}
