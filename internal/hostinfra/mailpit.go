package hostinfra

// Mailpit as a HOST PROCESS: the dev mail catcher, supervised like every other
// supervised-binary engine (supervised.go). The dev IdP sends its mail here
// (invitations, verification, password reset, magic links), and the developer
// reads it in Mailpit's web UI, so a sign-in flow that needs an email works in
// dev end to end with nothing leaving the machine.
//
// # What is not obvious
//
//   - `port` is the SMTP port, the one the IdP dials (plain SMTP, no TLS, no
//     auth: it is loopback only). The web UI and API listen on port+7000,
//     which puts the scaffold's 1025 on 8025, Mailpit's own defaults, and
//     keeps two stacks with distinct SMTP ports from colliding on the UI.
//   - The messages PERSIST (`--database` under the data directory), like every
//     other engine's state, so an invitation sent before a restart can still
//     be opened after it. Delete the data directory to start clean.
//   - Mailpit checks GitHub for a newer release on start and on `version`.
//     Both are switched off: a dev loop must not reach the network to start,
//     and the version check is how the cached binary is verified.
//   - Readiness is Mailpit's own /readyz on the UI port AND an SMTP banner on
//     the declared port, because the IdP is about to dial the latter.

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// EngineMailpit matches the `engine` discriminator on the KCL HostInfra schema.
const EngineMailpit = "mailpit"

// MailpitVersion is the pinned Mailpit release.
const MailpitVersion = "v1.33.0"

// mailpitUIPortOffset places the web UI relative to the SMTP port.
const mailpitUIPortOffset = 7000

// mailpitChecksums is each release archive's sha256, as GitHub reports it for
// the release's assets (Mailpit publishes no separate checksum file):
//
//	https://api.github.com/repos/axllent/mailpit/releases/tags/v1.33.0
var mailpitChecksums = map[string]string{
	"mailpit-darwin-amd64.tar.gz": "6f07a68ba48fc569ca4a2bff7871377f71dc4b5c24849fa25da17740989936e4",
	"mailpit-darwin-arm64.tar.gz": "0c4abbf289c6d8ee3c11adc2f93d9a1b45084e25104e19a0d9b06e847e98e0fb",
	"mailpit-linux-amd64.tar.gz":  "d0ec50080cbf3e7c1fa2cda2ee68125d7894175811084b1f8f47c6cfc6f487c8",
	"mailpit-linux-arm64.tar.gz":  "1d27c819e1a5dd2761ed9fd07022443e35a47793b2e92de5b49cf3656a649ef9",
	"mailpit-windows-amd64.zip":   "47122dd828ff80dcaef62b2952c478b178afadadf066c990489608e537121bfd",
	"mailpit-windows-arm64.zip":   "467c480c53d9f29626326f6187acceaaa6131204e632f638fa1d40ef1c578dea",
}

var mailpitRelease = binaryRelease{
	engine:    EngineMailpit,
	version:   MailpitVersion,
	checksums: mailpitChecksums,
	asset: func(goos, goarch string) string {
		ext := ".tar.gz"
		if goos == "windows" {
			ext = ".zip"
		}
		return fmt.Sprintf("mailpit-%s-%s%s", goos, goarch, ext)
	},
	url: func(asset string) string {
		return fmt.Sprintf("https://github.com/axllent/mailpit/releases/download/%s/%s", MailpitVersion, asset)
	},
	binary: func(goos string) string {
		if goos == "windows" {
			return "mailpit.exe"
		}
		return "mailpit"
	},
	versionArgs:   []string{"version", "--no-release-check"},
	versionNeedle: "mailpit " + MailpitVersion,
	approxSize:    "~15 MB",
}

var mailpitEngine = supervisedEngine{
	release:      mailpitRelease,
	args:         mailpitArgs,
	ready:        func(ctx context.Context, spec Spec, _ string) bool { return mailpitReady(ctx, spec) },
	readyTimeout: time.Minute,
}

// MailpitUIPort is the port a mailpit instance's web UI and API listen on.
func MailpitUIPort(spec Spec) int { return spec.Port + mailpitUIPortOffset }

// MailpitUIURL is the web UI's address, the one `forge env up` prints.
func MailpitUIURL(spec Spec) string { return "http://localhost:" + strconv.Itoa(MailpitUIPort(spec)) }

// mailpitArgs is the server's argv: SMTP on the declared loopback port, the
// UI beside it, messages persisted under the data directory.
func mailpitArgs(spec Spec, dataDir string) []string {
	return []string{
		"--smtp", "127.0.0.1:" + strconv.Itoa(spec.Port),
		"--listen", "127.0.0.1:" + strconv.Itoa(MailpitUIPort(spec)),
		"--database", filepath.Join(dataDir, "mailpit.db"),
		"--disable-version-check",
	}
}

// validateMailpit refuses a declaration whose UI port cannot exist.
func validateMailpit(spec Spec) error {
	if spec.Port < 1 || MailpitUIPort(spec) > 65535 {
		return fmt.Errorf("host-infra %s: mailpit's web UI listens on port+%d, so port must be at most %d (got %d)",
			spec.Name, mailpitUIPortOffset, 65535-mailpitUIPortOffset, spec.Port)
	}
	return nil
}

// mailpitReady performs one readiness probe: the UI answers /readyz and the
// SMTP port greets as Mailpit.
func mailpitReady(ctx context.Context, spec Spec) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(MailpitUIPort(spec))+"/readyz", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(probeCtx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(spec.Port)))
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	return err == nil && mailpitBanner(line)
}

// mailpitBanner reports whether an SMTP greeting is Mailpit's.
func mailpitBanner(line string) bool {
	return strings.HasPrefix(line, "220 ") && strings.Contains(line, "Mailpit")
}
