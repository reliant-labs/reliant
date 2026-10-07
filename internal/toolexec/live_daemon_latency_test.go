//go:build liveprobe

// Copyright (c) 2025 Reliant Labs

package toolexec

// Live latency harness for the daemon command path:
//
//	this test --NATS--> daemon-gateway (NATSToolBridge) --gRPC stream--> daemon
//
// It speaks the exact envelope NATSDaemonRouter.SendDaemonCommandToDaemon
// publishes, so the numbers are the router's round trip minus daemon
// RESOLUTION (which is a separate cost, measured elsewhere). It never runs in
// CI: it needs a running stack and a connected daemon.
//
//	PROBE_USER_ID=<user> PROBE_DAEMON_ID=<daemon> \
//	NATS_URL=nats://localhost:4222 NATS_USER=... NATS_PASSWORD=... \
//	PROBE_PATH=/path/the/greenfield/probe/scans \
//	  go test -tags liveprobe -run TestLiveDaemonCommandLatency -v -count=1 ./internal/toolexec/
//
// Every command it sends is read-only.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func liveProbeEnv(t *testing.T, key, fallback string) string {
	t.Helper()
	if v := os.Getenv(key); v != "" {
		return v
	}
	if fallback == "" {
		t.Skipf("%s not set", key)
	}
	return fallback
}

type liveProbe struct {
	nc       *nats.Conn
	userID   string
	daemonID string
}

// roundTrip sends one daemon.command and returns its wall-clock latency.
func (p *liveProbe) roundTrip(commandType string, payload any, timeout time.Duration) (time.Duration, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	envelope, err := json.Marshal(daemonCommandWire{
		RequestID:   newRequestID(),
		CommandType: commandType,
		Payload:     raw,
		TimeoutMs:   int32(timeout / time.Millisecond),
	})
	if err != nil {
		return 0, err
	}
	msg := &nats.Msg{Subject: daemonSubject(daemonCommandSubject, p.userID, p.daemonID), Data: envelope}

	start := time.Now()
	reply, err := requestWithChunkedReply(p.nc, msg, timeout)
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, err
	}
	var resp struct {
		Success      bool   `json:"success"`
		ErrorMessage string `json:"error_message"`
	}
	if err := json.Unmarshal(reply.Data, &resp); err != nil {
		return elapsed, fmt.Errorf("decode reply (%d bytes): %w", len(reply.Data), err)
	}
	if !resp.Success {
		return elapsed, fmt.Errorf("%s failed: %s", commandType, resp.ErrorMessage)
	}
	return elapsed, nil
}

func summarize(label string, samples []time.Duration) string {
	if len(samples) == 0 {
		return label + ": no samples"
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	pct := func(q float64) time.Duration { return sorted[int(q*float64(len(sorted)-1))] }
	r := func(d time.Duration) string { return d.Round(100 * time.Microsecond).String() }
	return fmt.Sprintf("%-46s n=%-3d min=%-9s p50=%-9s p90=%-9s max=%s",
		label, len(sorted), r(sorted[0]), r(pct(0.5)), r(pct(0.9)), r(sorted[len(sorted)-1]))
}

func TestLiveDaemonCommandLatency(t *testing.T) {
	natsURL := liveProbeEnv(t, "NATS_URL", nats.DefaultURL)
	probe := &liveProbe{
		userID:   liveProbeEnv(t, "PROBE_USER_ID", ""),
		daemonID: liveProbeEnv(t, "PROBE_DAEMON_ID", ""),
	}
	scanPath := liveProbeEnv(t, "PROBE_PATH", "")
	n, _ := strconv.Atoi(liveProbeEnv(t, "PROBE_N", "20"))

	opts := []nats.Option{nats.Name("daemon-latency-probe")}
	if u, pw := os.Getenv("NATS_USER"), os.Getenv("NATS_PASSWORD"); u != "" {
		opts = append(opts, nats.UserInfo(u, pw))
	}
	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		t.Fatalf("connect %s: %v", natsURL, err)
	}
	defer nc.Close()
	probe.nc = nc

	emptyDir := t.TempDir()
	const timeout = 10 * time.Second

	run := func(label, cmd string, payload any) []time.Duration {
		var samples []time.Duration
		for i := 0; i < n; i++ {
			d, err := probe.roundTrip(cmd, payload, timeout)
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			samples = append(samples, d)
		}
		t.Log(summarize(label, samples))
		return samples
	}

	// 1. Transport floor: a scan of an empty directory does no work, so this
	//    is NATS -> gateway -> daemon stream -> back, and nothing else.
	run("code_presence(empty dir)  [transport floor]", "project.code_presence", map[string]string{"path": emptyDir})

	// 2. The real probe the greenfield path sends.
	run("code_presence("+filepath.Base(scanPath)+")  [real probe]", "project.code_presence", map[string]string{"path": scanPath})

	// 3. Head-of-line: the same fast probe while a bulky read-only command is
	//    in flight on the same daemon stream. PROBE_BULK_FILE names a large
	//    file; the daemon reads it and ships it back over the stream.
	bulkFile := os.Getenv("PROBE_BULK_FILE")
	if bulkFile == "" {
		t.Log("PROBE_BULK_FILE not set; skipping the concurrent-load section")
		return
	}
	var (
		mu        sync.Mutex
		underLoad []time.Duration
		bulkTimes []time.Duration
	)
	for i := 0; i < n; i++ {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := probe.roundTrip("fs.read_binary_file", map[string]any{"path": bulkFile}, 60*time.Second)
			if err != nil {
				t.Errorf("bulk read: %v", err)
				return
			}
			mu.Lock()
			bulkTimes = append(bulkTimes, d)
			mu.Unlock()
		}()
		// Let the bulk command reach the daemon first, as unrelated work would.
		time.Sleep(5 * time.Millisecond)
		d, err := probe.roundTrip("project.code_presence", map[string]string{"path": emptyDir}, timeout)
		if err != nil {
			t.Fatalf("probe under load: %v", err)
		}
		underLoad = append(underLoad, d)
		wg.Wait()
	}
	t.Log(summarize("fs.read_binary_file(bulk)  [concurrent load]", bulkTimes))
	t.Log(summarize("code_presence(empty dir) while bulk in flight", underLoad))
}
