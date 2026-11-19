package templates

import (
	"strings"
	"testing"
)

func TestPGOBuildArg(t *testing.T) {
	for dir, want := range map[string]string{
		"app":        "FORGE_PGO_APP",
		"my-app":     "FORGE_PGO_MY_APP",
		"control.v2": "FORGE_PGO_CONTROL_V2",
		"Proxy9":     "FORGE_PGO_PROXY9",
	} {
		if got := PGOBuildArg(dir); got != want {
			t.Errorf("PGOBuildArg(%q) = %q, want %q", dir, got, want)
		}
	}
}

// TestDockerfile_PGOArgPerBinary pins the contract `forge build` relies on
// to turn one binary's PGO off: every binary's `go build` reads its own
// FORGE_PGO_<BIN> ARG, which defaults to auto (Go's default, so an image
// built without the arg behaves exactly as before). The debug builder is a
// stage of its own, which inherits no ARG, so it declares the primary
// binary's again: a binary whose PGO is off must not read its default.pgo
// in its debug build either.
func TestDockerfile_PGOArgPerBinary(t *testing.T) {
	data := struct {
		Name                   string
		DockerBuilderGoVersion string
		GoVersion              string
		VersionVar             string
		Binaries               []struct {
			Dir     string
			Primary bool
		}
	}{
		Name: "demo", DockerBuilderGoVersion: "1.27", GoVersion: "1.27",
		Binaries: []struct {
			Dir     string
			Primary bool
		}{{Dir: "demo", Primary: true}, {Dir: "edge-proxy"}},
	}
	src := string(renderProject(t, "Dockerfile.tmpl", data))
	builder := src[:strings.Index(src, "AS debug-builder")]
	for _, bin := range []struct{ dir, arg string }{{"demo", "FORGE_PGO_DEMO"}, {"edge-proxy", "FORGE_PGO_EDGE_PROXY"}} {
		if !strings.Contains(builder, "ARG "+bin.arg+"=auto\n") {
			t.Errorf("builder stage must declare ARG %s=auto:\n%s", bin.arg, builder)
		}
		line := "go build -pgo=${" + bin.arg + "} -trimpath"
		i := strings.Index(builder, line)
		if i < 0 || !strings.Contains(builder[i:strings.Index(builder[i:], "\n")+i], "./cmd/"+bin.dir) {
			t.Errorf("the go build of ./cmd/%s must pass -pgo=${%s}:\n%s", bin.dir, bin.arg, builder)
		}
	}
	debugStage := src[strings.Index(src, "AS debug-builder"):strings.Index(src, "AS dev")]
	if !strings.Contains(debugStage, "ARG FORGE_PGO_DEMO=auto\n") {
		t.Errorf("the debug builder must declare the primary binary's PGO arg:\n%s", debugStage)
	}
	if !strings.Contains(debugStage, "go build -pgo=${FORGE_PGO_DEMO} -trimpath -buildvcs=true -gcflags=\"all=-N -l\"") {
		t.Errorf("the debug build must pass -pgo=${FORGE_PGO_DEMO}:\n%s", debugStage)
	}
	if strings.Contains(debugStage, "FORGE_PGO_EDGE_PROXY") {
		t.Errorf("the debug builder builds only the primary binary:\n%s", debugStage)
	}
}
