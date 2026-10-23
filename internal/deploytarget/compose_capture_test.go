package deploytarget

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/reliant-labs/forge/internal/statefile"
)

type inventoryRunner struct {
	fakeRunner
	inventory string
}

func (r *inventoryRunner) OutputWithEnv(ctx context.Context, env map[string]string, name string, args ...string) ([]byte, error) {
	if len(args) >= 3 && args[len(args)-1] == "--quiet" {
		r.record(name, args)
		return []byte(r.inventory), nil
	}
	return r.fakeRunner.OutputWithEnv(ctx, env, name, args...)
}
func (r *inventoryRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) >= 3 && args[len(args)-1] == "--quiet" {
		r.record(name, args)
		return []byte(r.inventory), nil
	}
	return r.fakeRunner.Output(ctx, name, args...)
}

func TestComposeCapturesDependenciesButNeverAdoptsPreexistingContainers(t *testing.T) {
	dir := t.TempDir()
	labels := func(service string) string {
		b, _ := json.Marshal([]map[string]any{{"Config": map[string]any{"Labels": map[string]string{"com.docker.compose.project": "forge-dev", "com.docker.compose.project.working_dir": dir, "com.docker.compose.service": service}}}})
		return string(b)
	}
	r := &inventoryRunner{inventory: "new-ui new-dependency foreign", fakeRunner: fakeRunner{outputs: map[string]string{"docker inspect new-ui": labels("clickstack"), "docker inspect new-dependency": labels("profiles-schema")}}}
	p := ComposeProvider{ProjectDir: dir, Runner: r}
	spec := &ComposeSpec{ProjectDirectory: dir}
	if err := p.recordOwned(context.Background(), r, nil, spec, "dev", "clickstack", []string{"foreign"}); err != nil {
		t.Fatal(err)
	}
	rec, err := statefile.Read[composeOwned](composeOwnedPath(dir, "dev", "clickstack"), "ownership")
	if err != nil || rec == nil || len(rec.IDs) != 2 || rec.Services["new-dependency"] != "profiles-schema" {
		t.Fatalf("dependency ownership missing: %+v %v", rec, err)
	}
	r.outputs["docker inspect --format {{.State.Running}} new-ui"] = "true"
	if running, err := p.OwnedRunning(context.Background(), "dev"); err != nil || !running {
		t.Fatalf("compose-only stack invisible: %v %v", running, err)
	}
	if running, err := p.OwnedRunning(context.Background(), "other"); err != nil || running {
		t.Fatalf("read another env: %v %v", running, err)
	}
	if err := p.StopOwned(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}
	for _, call := range r.calls {
		if call == "docker rm -f foreign" {
			t.Fatal("removed unowned preexisting container")
		}
	}
}
