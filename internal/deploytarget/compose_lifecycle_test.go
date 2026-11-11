package deploytarget

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/statefile"
)

func TestComposeDownExactCheckoutEnvAndRecordedContainers(t *testing.T) {
	dir := t.TempDir()
	record := composeOwned{Directory: dir, Checkout: dir, Env: "dev", Project: "project-dev-feature", Service: "clickstack", IDs: []string{"ours", "reused"}}
	for _, env := range []string{"dev", "other"} {
		if err := statefile.Write(composeOwnedPath(dir, env, "clickstack"), "compose ownership", record); err != nil {
			t.Fatal(err)
		}
	}
	label := func(directory, project string) string {
		b, _ := json.Marshal([]map[string]any{{"Config": map[string]any{"Labels": map[string]string{"com.docker.compose.project": project, "com.docker.compose.project.working_dir": directory, "com.docker.compose.service": "clickstack"}}}})
		return string(b)
	}
	r := &fakeRunner{outputs: map[string]string{"docker inspect ours": label(dir, record.Project), "docker inspect reused": label(filepath.Join(dir, "another"), record.Project)}}
	p := ComposeProvider{ProjectDir: dir, Runner: r}
	if err := p.StopOwned(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(r.calls, "\n")
	if !strings.Contains(joined, "docker rm -f ours") {
		t.Fatalf("owned container forgotten: %s", joined)
	}
	if strings.Contains(joined, "rm -f reused") || strings.Contains(joined, "-v") || strings.Contains(joined, "down") {
		t.Fatalf("overbroad teardown: %s", joined)
	}
	other, err := statefile.Read[composeOwned](composeOwnedPath(dir, "other", "clickstack"), "compose ownership")
	if err != nil || other == nil {
		t.Fatalf("other env record lost: %v", err)
	}
}

// TestComposeDownSurvivesAGoneOrUnverifiableContainer pins the env-down
// regression from a ClickStack + otel-profiles stack: compose had replaced a
// recorded container, Docker 29 reported it as "error: no such object" (lower
// case), the check treated that as a failure, and StopOwned returned before
// reaching otel-profiles and profiles-schema, which kept running.
func TestComposeDownSurvivesAGoneOrUnverifiableContainer(t *testing.T) {
	dir := t.TempDir()
	const project = "forge-abc-dev"
	write := func(service string, ids ...string) {
		t.Helper()
		services := map[string]string{}
		for _, id := range ids {
			services[id] = service
		}
		rec := composeOwned{Directory: dir, Checkout: dir, Env: "dev", Project: project, Service: service, IDs: ids, Services: services}
		if err := statefile.Write(composeOwnedPath(dir, "dev", service), "compose ownership", rec); err != nil {
			t.Fatal(err)
		}
	}
	// Records glob in name order, so the failures sit in the FIRST record:
	// stopping there is exactly the leak this test exists for.
	write("clickstack", "a-replaced", "b-docker28-gone", "c-clickstack", "d-unverifiable")
	write("otel-profiles", "e-otel", "f-foreign")
	write("profiles-schema", "g-exited-schema")

	label := func(directory, project, service string) string {
		b, _ := json.Marshal([]map[string]any{{"Config": map[string]any{"Labels": map[string]string{"com.docker.compose.project": project, "com.docker.compose.project.working_dir": directory, "com.docker.compose.service": service}}}})
		return string(b)
	}
	r := &fakeRunner{
		outputs: map[string]string{
			"docker inspect a-replaced":      "[]\nerror: no such object: a-replaced\n",
			"docker inspect b-docker28-gone": "[]\nError: No such object: b-docker28-gone\n",
			"docker inspect c-clickstack":    label(dir, project, "clickstack"),
			"docker inspect d-unverifiable":  "permission denied while trying to connect to the Docker daemon socket",
			"docker inspect e-otel":          label(dir, project, "otel-profiles"),
			"docker inspect f-foreign":       label(dir, "someone-else", "otel-profiles"),
			"docker inspect g-exited-schema": label(dir, project, "profiles-schema"),
		},
		runErrs: map[string]error{
			"docker inspect a-replaced":      errors.New("exit status 1"),
			"docker inspect b-docker28-gone": errors.New("exit status 1"),
			"docker inspect d-unverifiable":  errors.New("exit status 1"),
		},
	}
	p := ComposeProvider{ProjectDir: dir, Runner: r}
	err := p.StopOwned(context.Background(), "dev")
	if err == nil || !strings.Contains(err.Error(), "d-unverifiable") {
		t.Fatalf("an unverifiable container must be reported, got %v", err)
	}
	if strings.Contains(err.Error(), "a-replaced") || strings.Contains(err.Error(), "b-docker28-gone") {
		t.Fatalf("a container that no longer exists is not a failure: %v", err)
	}

	removed := map[string]bool{}
	for _, call := range r.calls {
		if id, ok := strings.CutPrefix(call, "docker rm -f "); ok {
			removed[id] = true
		}
	}
	for _, id := range []string{"c-clickstack", "e-otel", "g-exited-schema"} {
		if !removed[id] {
			t.Errorf("owned container %s left running; calls:\n%s", id, strings.Join(r.calls, "\n"))
		}
	}
	for _, id := range []string{"a-replaced", "b-docker28-gone", "d-unverifiable", "f-foreign"} {
		if removed[id] {
			t.Errorf("removed %s, whose ownership was not proven", id)
		}
	}

	// The unverified ID stays recorded so the next down retries it; settled
	// records are gone.
	rec, err := statefile.Read[composeOwned](composeOwnedPath(dir, "dev", "clickstack"), "compose ownership")
	if err != nil || rec == nil || !slices.Equal(rec.IDs, []string{"d-unverifiable"}) || len(rec.Services) != 1 {
		t.Fatalf("clickstack record should keep only the unverified ID: %+v %v", rec, err)
	}
	for _, service := range []string{"otel-profiles", "profiles-schema"} {
		if rec, err := statefile.Read[composeOwned](composeOwnedPath(dir, "dev", service), "compose ownership"); err != nil || rec != nil {
			t.Errorf("%s record should be deleted once settled: %+v %v", service, rec, err)
		}
	}
}

func TestContainerAbsentMatchesEveryDockerSpelling(t *testing.T) {
	for out, want := range map[string]bool{
		"error: no such object: 3f2a":                      true, // Docker 29
		"Error: No such object: 3f2a":                      true, // Docker ≤ 28
		"Error response from daemon: No such container: x": true,
		"permission denied while trying to connect":        false,
		"Cannot connect to the Docker daemon":              false,
	} {
		if got := containerAbsent([]byte(out)); got != want {
			t.Errorf("containerAbsent(%q) = %v, want %v", out, got, want)
		}
	}
}
