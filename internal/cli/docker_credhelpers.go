package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/dockercred"
)

// dockerCredHelpersOnce runs the credential-helper check once per process:
// one forge command may build several images.
var dockerCredHelpersOnce sync.Once

// checkDockerCredHelpers runs before forge's first docker build. A docker
// config that names a credential helper whose docker-credential-<name> is not
// on PATH fails every build that pulls from or pushes to its hosts, and the
// failure arrived minutes into the build ("error getting credentials - err:
// exec: \"docker-credential-gcloud\": executable file not found in $PATH",
// obs 51b41070).
//
// When the Cloud SDK's helper sits beside the gcloud on PATH (reached through
// a symlink, so its directory is not on PATH), that directory is appended to this
// process's PATH (after every directory already on it), which the docker children inherit, and a note says so.
// Otherwise a warning names the helper, its hosts and the fix; the build still
// runs, because one that never touches those hosts does not need the helper.
func checkDockerCredHelpers(w io.Writer) {
	dockerCredHelpersOnce.Do(func() {
		reportDockerCredHelpers(w, dockercred.ConfigPath(), exec.LookPath)
	})
}

func reportDockerCredHelpers(w io.Writer, configPath string, lookPath func(string) (string, error)) {
	if configPath == "" {
		return
	}
	for _, m := range dockercred.MissingHelpers(configPath, lookPath) {
		bin := "docker-credential-" + m.Helper
		if dir := dockercred.HelperBesideTool(m.Helper, lookPath); dir != "" {
			// Appended, never prepended: the directory holds the SDK's other
			// tools too, and the ones already on PATH must keep winning.
			_ = os.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+dir)
			_, _ = fmt.Fprintf(w, "[build] %s names %s, which is not on PATH; using the one beside gcloud in %s\n", configPath, bin, dir)
			continue
		}
		scope := "every registry without a credHelpers entry (credsStore)"
		if len(m.Hosts) > 0 {
			scope = strings.Join(m.Hosts, ", ")
		}
		fix := "install it, or put the directory that holds it on PATH"
		if m.Helper == "gcloud" {
			fix = "put the Cloud SDK's bin directory on PATH (`gcloud info --format='value(installation.sdk_root)'`/bin)"
		}
		_, _ = fmt.Fprintf(w, "warning: %s sends credentials for %s to %s, which is not on PATH: a docker build or push that touches them fails with \"error getting credentials\". Fix: %s\n",
			configPath, scope, bin, fix)
	}
}
