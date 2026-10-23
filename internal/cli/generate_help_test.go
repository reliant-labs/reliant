package cli

import (
	"strings"
	"testing"
)

// `forge generate --help` is the first thing an agent reads about generate,
// so it must describe what generate does NOW. It described entity protos in
// proto/db/ and a pkg/app/bootstrap.go long after SQL migrations became the
// schema and the composition graph replaced bootstrap.go.
func TestGenerateHelp_DescribesTheCurrentPipeline(t *testing.T) {
	long := newGenerateCmd().Long
	for _, retired := range []string{
		"proto/db",      // entities are projected from db/migrations
		"proto/api",     // no directory convention by that name
		"entity protos", // (forge.v1.entity) is retired
		"bootstrap.go",  // replaced by internal/app's composition
		"pkg/app/",      // the composition root is internal/app/
		"directory convention scanning",
	} {
		if strings.Contains(long, retired) {
			t.Errorf("generate --help still names %q, which forge no longer has:\n%s", retired, long)
		}
	}
	for _, current := range []string{
		"db/migrations",
		"gen/forge_descriptor.json",
		"internal/handlers/",
		"internal/db/",
		"internal/app/",
		"compose.go",
		"go build",
	} {
		if !strings.Contains(long, current) {
			t.Errorf("generate --help does not mention %q:\n%s", current, long)
		}
	}
}
