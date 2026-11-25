package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// goneContainerDocker is a Docker whose `ps -aq` lists three containers and
// whose `inspect` knows only the ones in live: asking for any other id fails
// the whole call with the CLI's no-such-object error and NO output, as the real
// one does when a stack comes down between the listing and the inspect.
type goneContainerDocker struct {
	live       map[string]string // id -> `inspect --format` output / json row
	inspectErr error             // returned for every inspect when set
	calls      []string
}

func (d *goneContainerDocker) command(_ context.Context, name string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	d.calls = append(d.calls, joined)
	switch {
	case joined == "ps -aq":
		return []byte("c0\nc1\nc2\n"), nil
	case args[0] == "inspect":
		if d.inspectErr != nil {
			return nil, d.inspectErr
		}
		var ids []string
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "{{") {
				ids = append(ids, a)
			}
		}
		var rows []string
		for _, id := range ids {
			row, ok := d.live[id]
			if !ok {
				return nil, fmt.Errorf("docker %v: exit status 1: Error: No such object: %s", args, id)
			}
			rows = append(rows, row)
		}
		if strings.Contains(joined, "--format") {
			return []byte(strings.Join(rows, "\n")), nil
		}
		return []byte("[" + strings.Join(rows, ",") + "]"), nil
	}
	return nil, fmt.Errorf("unexpected %s %s", name, joined)
}

// TestInspectSkipsContainersRemovedMidRun: `forge storage gc --apply` exited 1
// because a container it had listed was gone by the time it inspected it.
func TestInspectSkipsContainersRemovedMidRun(t *testing.T) {
	ctx := context.Background()

	t.Run("volume use", func(t *testing.T) {
		d := &goneContainerDocker{live: map[string]string{"c0": "vol-a", "c2": "vol-b"}}
		used, err := Runner{Command: d.command}.containerVolumeUse(ctx)
		if err != nil {
			t.Fatalf("a vanished container failed the plan: %v", err)
		}
		if !used["vol-a"] || !used["vol-b"] || len(used) != 2 {
			t.Fatalf("used = %v, want the two surviving containers' volumes", used)
		}
	})

	t.Run("image use", func(t *testing.T) {
		d := &goneContainerDocker{live: map[string]string{"c1": "sha256:one ref:1"}}
		used, err := Runner{Command: d.command}.containerImageUse(ctx)
		if err != nil || !used["sha256:one"] || !used["ref:1"] {
			t.Fatalf("used = %v, err = %v", used, err)
		}
	})

	t.Run("json inspect", func(t *testing.T) {
		d := &goneContainerDocker{live: map[string]string{
			"c0": `{"Name":"/web","HostConfig":{"PortBindings":{"80/tcp":[{"HostPort":"8080"}]}}}`,
			"c2": `{"Name":"/db","HostConfig":{"PortBindings":{}}}`,
		}}
		ports, names, err := Runner{Command: d.command}.backedLocalRegistries(ctx)
		if err != nil {
			t.Fatalf("a vanished container failed the plan: %v", err)
		}
		if !ports["8080"] || !names["web"] || !names["db"] || len(names) != 2 {
			t.Fatalf("ports=%v names=%v", ports, names)
		}
	})

	t.Run("every container gone is an empty answer", func(t *testing.T) {
		d := &goneContainerDocker{live: map[string]string{}}
		used, err := Runner{Command: d.command}.containerVolumeUse(ctx)
		if err != nil || len(used) != 0 {
			t.Fatalf("used = %v, err = %v", used, err)
		}
	})

	t.Run("a healthy listing is one inspect call", func(t *testing.T) {
		d := &goneContainerDocker{live: map[string]string{"c0": "a", "c1": "b", "c2": "c"}}
		if _, err := (Runner{Command: d.command}).containerVolumeUse(ctx); err != nil {
			t.Fatal(err)
		}
		if n := len(d.calls); n != 2 { // ps, one inspect
			t.Fatalf("%d docker calls %q, want ps and a single bulk inspect", n, d.calls)
		}
	})

	t.Run("any other inspect failure still fails", func(t *testing.T) {
		d := &goneContainerDocker{live: map[string]string{"c0": "a"}, inspectErr: errors.New("Cannot connect to the Docker daemon")}
		if _, err := (Runner{Command: d.command}).containerVolumeUse(ctx); err == nil {
			t.Fatal("a daemon error must not be read as 'containers gone'")
		}
	})
}
