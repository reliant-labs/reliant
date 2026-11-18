package hostinfra

// NATS as a HOST PROCESS: nats-server with JetStream, supervised like every
// other supervised-binary engine (supervised.go).
//
// # What is not obvious
//
//   - JetStream is always on, with its store under the data directory. A dev
//     server without it would accept connections and then fail the first
//     stream or KV call an app makes, which is a worse failure than any
//     resource it saves.
//   - It binds loopback only and takes NO credentials. The HostInfra
//     declaration's user/password fields describe postgres and are ignored
//     here: an auth-enabled dev NATS would make every app connect twice as
//     carefully for no isolation a loopback socket does not already give.
//   - Readiness reads the server's INFO line rather than dialling the port.
//     NATS writes INFO the moment a client connects, and it carries
//     `"jetstream":true` only once JetStream is up, so one read answers "is it
//     NATS, and can it serve streams" without a monitoring port.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// EngineNATS matches the `engine` discriminator on the KCL HostInfra schema.
const EngineNATS = "nats"

// NATSVersion is the pinned nats-server release.
const NATSVersion = "v2.15.1"

// natsChecksums is each release archive's sha256, copied from
//
//	https://github.com/nats-io/nats-server/releases/download/v2.15.1/SHA256SUMS
var natsChecksums = map[string]string{
	"nats-server-v2.15.1-darwin-amd64.tar.gz": "3cba5976a4fbd022669d3401cab4147faeb2b4c079712c882edb9ae4e52feb06",
	"nats-server-v2.15.1-darwin-arm64.tar.gz": "ed0fe39222ec2e317c4d0e1a9cdc16c63ec523d94f9355d24a902a4db2655ffd",
	"nats-server-v2.15.1-linux-amd64.tar.gz":  "bf5edc7b1ce98e885c368cb43a886eb148dc5d0551d99f3cf6141fb5e1954c70",
	"nats-server-v2.15.1-linux-arm64.tar.gz":  "e2fbab4cad7d3c76e3052eb959570393c9d73ba756b70bf8e9eea61e9ae6d49c",
	"nats-server-v2.15.1-windows-amd64.zip":   "4f7edfa376750b2720463bd22766ed469a5f13f378a643097866fcd953cbf7e3",
	"nats-server-v2.15.1-windows-arm64.zip":   "888a62b665b3123edde84c2e39751e4d9ced1e0dc7a7a234a40c53f2bba40b6f",
}

var natsRelease = binaryRelease{
	engine:    EngineNATS,
	version:   NATSVersion,
	checksums: natsChecksums,
	asset: func(goos, goarch string) string {
		ext := ".tar.gz"
		if goos == "windows" {
			ext = ".zip"
		}
		return fmt.Sprintf("nats-server-%s-%s-%s%s", NATSVersion, goos, goarch, ext)
	},
	url: func(asset string) string {
		return fmt.Sprintf("https://github.com/nats-io/nats-server/releases/download/%s/%s", NATSVersion, asset)
	},
	binary: func(goos string) string {
		if goos == "windows" {
			return "nats-server.exe"
		}
		return "nats-server"
	},
	versionArgs:   []string{"--version"},
	versionNeedle: NATSVersion,
	approxSize:    "~7 MB",
}

var natsEngine = supervisedEngine{
	release:      natsRelease,
	args:         natsArgs,
	ready:        func(ctx context.Context, spec Spec, _ string) bool { return natsReady(ctx, spec.Port) },
	readyTimeout: time.Minute,
}

// natsArgs is the server's argv: loopback, the declared client port,
// JetStream stored under the data directory, and the monitoring endpoint only
// when one is declared.
func natsArgs(spec Spec, dataDir string) []string {
	args := []string{
		"--addr", "127.0.0.1",
		"--port", strconv.Itoa(spec.Port),
		"--server_name", spec.Name,
		"--jetstream",
		"--store_dir", filepath.Join(dataDir, "jetstream"),
	}
	if spec.NATSMonitorPort > 0 {
		args = append(args, "--http_port", strconv.Itoa(spec.NATSMonitorPort))
	}
	return args
}

// natsReady performs one readiness probe: a NATS server whose INFO says
// JetStream is enabled.
func natsReady(ctx context.Context, port int) bool {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return false
	}
	return natsInfoReady(line)
}

// natsInfoReady reports whether a server's first line is an INFO with
// JetStream enabled.
func natsInfoReady(line string) bool {
	payload, ok := strings.CutPrefix(strings.TrimSpace(line), "INFO ")
	if !ok {
		return false
	}
	var info struct {
		JetStream bool `json:"jetstream"`
	}
	if err := json.Unmarshal([]byte(payload), &info); err != nil {
		return false
	}
	return info.JetStream
}
