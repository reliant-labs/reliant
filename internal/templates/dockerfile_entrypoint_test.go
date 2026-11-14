package templates

import (
	"regexp"
	"strings"
	"testing"
)

// TestDockerfile_ArgsSelectTheSubcommand pins the contract every runtime
// relies on (ADR 0002): a workload's `args` select a subcommand of the
// project binary. In a pod, Kubernetes `args` REPLACE the image's CMD, so the
// image must carry the binary as its ENTRYPOINT and only the default
// subcommand as CMD. With the old `CMD ["./<bin>", "server"]` and no
// ENTRYPOINT, `args = ["item"]` would exec a program called `item`.
func TestDockerfile_ArgsSelectTheSubcommand(t *testing.T) {
	src := string(renderProject(t, "Dockerfile.tmpl", projectData()))
	production := src[strings.Index(src, "AS production"):]

	if !regexp.MustCompile(`(?m)^ENTRYPOINT \["/app/demo"\]$`).MatchString(production) {
		t.Errorf("production stage must ENTRYPOINT the binary (/app/demo), so a workload's args are its subcommand:\n%s", production)
	}
	if !regexp.MustCompile(`(?m)^CMD \["server"\]$`).MatchString(production) {
		t.Errorf("production stage's CMD must be only the default subcommand [\"server\"]:\n%s", production)
	}
	if strings.Contains(production, `CMD ["./demo"`) {
		t.Errorf("production stage still names the binary in CMD; args would replace it")
	}

	debug := src[strings.Index(src, "AS debug\n"):strings.Index(src, "AS production")]
	if !strings.Contains(debug, `"/app/demo", "--"]`) || !regexp.MustCompile(`(?m)^CMD \["server"\]$`).MatchString(debug) {
		t.Errorf("debug stage must pass the subcommand (CMD) to the program after dlv's `--`:\n%s", debug)
	}
}
