package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// `docker compose exec clickstack` must name the project `forge env up`
// started the env's ClickStack in; the bare default project is one env up
// never starts, so every local query reported "not running".
func TestLocalClickstackComposeArgsNamesTheEnvsProject(t *testing.T) {
	old := composeLabelsFn
	t.Cleanup(func() { composeLabelsFn = old })
	dir := t.TempDir()
	envProject := "forge-" + projectIDForDir(dir) + "-dev"

	for name, tc := range map[string]struct {
		running []string
		err     error
		want    string
	}{
		"the env's own project":           {running: []string{"forge-x-other", envProject}, want: envProject},
		"an author-named project, alone":  {running: []string{"mystack", "mystack"}, want: "mystack"},
		"none running: the env's default": {want: envProject},
		"docker unavailable":              {err: errors.New("no docker"), want: envProject},
		"ambiguous: the env's default":    {running: []string{"a", "b"}, want: envProject},
	} {
		t.Run(name, func(t *testing.T) {
			composeLabelsFn = func(context.Context, string, string) ([]string, error) { return tc.running, tc.err }
			if got := strings.Join(localClickstackComposeArgs(context.Background(), dir, "dev"), " "); got != "-p "+tc.want {
				t.Fatalf("args = %q, want -p %s", got, tc.want)
			}
		})
	}
}
