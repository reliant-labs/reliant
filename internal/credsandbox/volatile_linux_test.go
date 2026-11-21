// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The private caches a credentials-none command writes into are overlays whose
// upper layer is thrown away when it exits, so they are mounted volatile. A
// plain overlay is unmounted, when the sandbox's namespace goes, with a syncfs
// of its whole upper filesystem: on a busy machine the command's init sat in D
// state for longer than the command's timeout, and a command that had finished
// came back exit 124 (TL-21).
func TestSandbox_ItsCacheOverlaysAreVolatile(t *testing.T) {
	testkit.Slow(t, "runs a sandboxed command with overlays")
	requireSandbox(t)
	home := wrapHome(t)
	gocache := filepath.Join(filepath.Dir(home), "gocache")
	require.NoError(t, os.MkdirAll(gocache, 0o755))
	project := filepath.Join(filepath.Dir(home), "project")
	require.NoError(t, os.MkdirAll(project, 0o755))

	out := runWrapped(t, "none", project, home, `grep ' - overlay ' /proc/self/mountinfo; true`, "GOCACHE="+gocache)
	var overlays []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		// Only the sandbox's own: the host's container overlays are in this
		// mount table too, and the sandbox always mounts with userxattr.
		if strings.Contains(line, " - overlay ") && strings.Contains(line, "userxattr") && !strings.Contains(line, "nouserxattr") {
			overlays = append(overlays, line)
		}
	}
	if len(overlays) == 0 {
		t.Skipf("this host gives the sandbox no overlays (withoutOverlays): %s", out)
	}
	for _, line := range overlays {
		assert.Contains(t, line, "volatile", "an overlay the sandbox mounted is not volatile: %s", line)
	}
	assert.True(t, strings.Contains(out, gocache), "GOCACHE is one of the overlays: %s", out)
}
