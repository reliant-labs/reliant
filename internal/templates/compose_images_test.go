package templates

import (
	"os"
	"regexp"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// digestPinned is <registry-host>/<path>[:tag]@sha256:<64 hex>.
var digestPinned = regexp.MustCompile(`^[^/]+/[^@\s]+@sha256:[0-9a-f]{64}$`)

// dockerHubImage reports whether ref pulls from Docker Hub: an explicit Hub
// host, or no registry host at all (a first path component without a dot or
// a port, other than localhost, is a Hub namespace — `postgres:17`,
// `clickhouse/clickstack-local:2.40.0`).
func dockerHubImage(ref string) bool {
	first, _, found := strings.Cut(ref, "/")
	if !found {
		return true
	}
	switch first {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		return true
	case "localhost":
		return false
	}
	return !strings.ContainsAny(first, ".:")
}

// TestScaffoldedComposeImagesAvoidDockerHubAndPinDigests pins the registry
// policy stated at the top of docker-compose.yml.tmpl. A scaffolded dev stack
// that pulls from Docker Hub fails its first `forge env up` wherever an IP is
// shared (an office, a CI runner, a cloud sandbox) once the anonymous pull
// limit runs out, and a tag without a digest makes a registry swap a
// content change.
func TestScaffoldedComposeImagesAvoidDockerHubAndPinDigests(t *testing.T) {
	data := struct {
		ProjectName string
		HasFrontend bool
	}{ProjectName: "shopdemo"}
	images := map[string]string{} // "<file>:<service>" -> image
	for name, category := range map[string]TemplateCategory{
		"docker-compose.yml.tmpl":         ProjectTemplates(),
		"e2e/docker-compose.e2e.yml.tmpl": TestTemplates(),
	} {
		out, err := category.Render(name, data)
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		var cf struct {
			Services map[string]struct {
				Image string `yaml:"image"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal(out, &cf); err != nil {
			t.Fatalf("%s is not valid YAML: %v", name, err)
		}
		n := 0
		for svc, s := range cf.Services {
			if s.Image == "" {
				continue // built locally
			}
			n++
			images[name+":"+svc] = s.Image
			if dockerHubImage(s.Image) {
				t.Errorf("%s service %s pulls %q from Docker Hub; use the publisher's registry, public.ecr.aws/docker/library or mirror.gcr.io", name, svc, s.Image)
			}
			if !digestPinned.MatchString(s.Image) {
				t.Errorf("%s service %s image %q is not pinned by digest", name, svc, s.Image)
			}
		}
		if n == 0 {
			t.Fatalf("%s has no image to check; the guard would be vacuous", name)
		}
	}

	// The node agent's collector (kcl/base.k) and the compose collectors are
	// one reference: base.k says so, and this keeps it true.
	base, err := os.ReadFile("../../kcl/base.k")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^OTEL_COLLECTOR_IMAGE: str = "([^"]+)"`).FindSubmatch(base)
	if m == nil {
		t.Fatal("kcl/base.k declares no OTEL_COLLECTOR_IMAGE")
	}
	for _, svc := range []string{"otel-forwarder", "otel-profiles"} {
		if got := images["docker-compose.yml.tmpl:"+svc]; got != string(m[1]) {
			t.Errorf("compose %s image %q != kcl/base.k OTEL_COLLECTOR_IMAGE %q", svc, got, m[1])
		}
	}
}

func TestDockerHubImage(t *testing.T) {
	for ref, want := range map[string]bool{
		"postgres:17-alpine":                        true,
		"clickhouse/clickstack-local:2.40.0":        true,
		"docker.io/library/postgres:17":             true,
		"public.ecr.aws/docker/library/postgres:17": false,
		"mirror.gcr.io/clickhouse/clickstack-local": false,
		"localhost:5050/api":                        false,
		"localhost/api":                             false,
		"quay.io/prometheus/alertmanager:v0.28.1":   false,
	} {
		if got := dockerHubImage(ref); got != want {
			t.Errorf("dockerHubImage(%q) = %v, want %v", ref, got, want)
		}
	}
}
