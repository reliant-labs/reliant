package templates

import (
	"strings"
	"testing"
)

// TestDockerfile_CoverStage pins the contract `forge build --target cover`
// relies on (kcl/schema.k Tests): a `cover` stage whose binaries are built
// with -cover -covermode=atomic and FORGE_COVERPKG, that writes coverage to
// /tmp (the emptyDir every forge-rendered pod mounts), and that leaves the
// production stage alone — production stays the LAST stage, so a plain
// `docker build` still builds it, and nothing in it is instrumented.
func TestDockerfile_CoverStage(t *testing.T) {
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
	cb, cs, prod := strings.Index(src, "AS cover-builder\n"), strings.Index(src, "AS cover\n"), strings.Index(src, "AS production\n")
	if cb < 0 || cs < 0 || prod < 0 || !(cb < cs && cs < prod) {
		t.Fatalf("want stages cover-builder, cover, then production (last); got indexes %d %d %d", cb, cs, prod)
	}
	if strings.Contains(src[prod:], "\nFROM ") {
		t.Errorf("production must stay the last stage, so a build with no --target is the production image")
	}
	builder := src[cb:cs]
	if !strings.Contains(builder, "ARG FORGE_COVERPKG=\n") {
		t.Errorf("the cover builder must declare FORGE_COVERPKG:\n%s", builder)
	}
	for _, bin := range []struct{ dir, arg string }{{"demo", "FORGE_PGO_DEMO"}, {"edge-proxy", "FORGE_PGO_EDGE_PROXY"}} {
		line := "go build -cover -covermode=atomic ${FORGE_COVERPKG:+-coverpkg=${FORGE_COVERPKG}} -pgo=${" + bin.arg + "}"
		if !strings.Contains(builder, line) || !strings.Contains(builder, "-o /app/cover/"+bin.dir+" ./cmd/"+bin.dir) {
			t.Errorf("the cover builder must build ./cmd/%s with %q:\n%s", bin.dir, line, builder)
		}
	}
	stage := src[cs:prod]
	for _, want := range []string{"COPY --from=cover-builder /app/cover/demo ./demo", "COPY --from=cover-builder /app/cover/edge-proxy ./edge-proxy", "ENV GOCOVERDIR=/tmp", "USER 65532:65532", `ENTRYPOINT ["/app/demo"]`} {
		if !strings.Contains(stage, want) {
			t.Errorf("the cover stage lacks %q:\n%s", want, stage)
		}
	}
	if p := src[prod:]; strings.Contains(p, "cover") || strings.Contains(p, "GOCOVERDIR") {
		t.Errorf("the production stage must not mention coverage:\n%s", p)
	}
}
