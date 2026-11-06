package doctor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CheckDelve verifies that the Delve debugger is reachable inside the
// app-debug container and its JSON-RPC API responds.
func CheckDelve(ctx context.Context, env *Environment) CheckResult {
	// Delve runs in the compose `app-debug` service. A project that declares
	// no compose file has no such service, so there is nothing to ask docker
	// — and asking anyway reported THIS machine's compose state for every
	// host-only project's `forge env status`.
	if !hasComposeFile(env.ProjectDir) {
		return CheckResult{
			Status:  StatusSkip,
			Message: "no compose file — Delve runs in the compose app-debug service",
		}
	}

	var evidence []string

	// Check for an existing debug session file.
	sessionFile := filepath.Join(env.ProjectDir, ".forge", "debug-session.json")
	if _, err := os.Stat(sessionFile); err == nil {
		evidence = append(evidence, "session file found")
	}

	// 1. Check whether app-debug service is running.
	psOut, err := env.compose(ctx, "ps", "--format", "json")
	if err != nil {
		return CheckResult{
			Status:   StatusFail,
			Message:  "failed to list containers",
			Evidence: strings.Join(append(evidence, fmt.Sprintf("docker compose ps: %v", err)), "\n"),
		}
	}

	if !serviceRunning(psOut, "app-debug") {
		return CheckResult{
			Status:   StatusSkip,
			Message:  "debug profile not active (use 'forge debug start')",
			Evidence: strings.Join(evidence, "\n"),
		}
	}

	// 2. Discover the Delve host port.
	portOut, err := env.compose(ctx, "port", "app-debug", "2345")
	if err != nil {
		return CheckResult{
			Status:   StatusFail,
			Message:  "could not discover Delve port",
			Evidence: strings.Join(append(evidence, fmt.Sprintf("docker compose port: %v", err)), "\n"),
		}
	}

	addr := strings.TrimSpace(string(portOut))
	if addr == "" {
		return CheckResult{
			Status:   StatusFail,
			Message:  "Delve port not mapped",
			Evidence: strings.Join(evidence, "\n"),
		}
	}

	env.SetPort("app-debug", 2345, addr)
	evidence = append(evidence, fmt.Sprintf("mapped port: %s", addr))

	// 3. Connect to Delve JSON-RPC API.
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return CheckResult{
			Status:   StatusWarn,
			Message:  fmt.Sprintf("Delve port %s open but connection failed", addr),
			Evidence: strings.Join(append(evidence, fmt.Sprintf("dial: %v", err)), "\n"),
		}
	}
	defer func() { _ = conn.Close() }()

	// Set a deadline for the entire RPC exchange.
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	req := `{"method":"RPCServer.GetVersion","params":[],"id":1}` + "\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return CheckResult{
			Status:   StatusWarn,
			Message:  fmt.Sprintf("Delve listening on %s but write failed", addr),
			Evidence: strings.Join(append(evidence, fmt.Sprintf("write: %v", err)), "\n"),
		}
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return CheckResult{
			Status:   StatusWarn,
			Message:  fmt.Sprintf("Delve listening on %s but no response", addr),
			Evidence: strings.Join(append(evidence, fmt.Sprintf("read: %v", err)), "\n"),
		}
	}

	var rpcResp struct {
		Result interface{} `json:"result"`
		Error  interface{} `json:"error"`
	}
	if err := json.Unmarshal(line, &rpcResp); err != nil || rpcResp.Result == nil {
		return CheckResult{
			Status:   StatusWarn,
			Message:  fmt.Sprintf("Delve listening on %s but unexpected response", addr),
			Evidence: strings.Join(append(evidence, fmt.Sprintf("response: %s", strings.TrimSpace(string(line)))), "\n"),
		}
	}

	return CheckResult{
		Status:   StatusPass,
		Message:  fmt.Sprintf("Delve listening on %s, API v2", addr),
		Evidence: strings.Join(evidence, "\n"),
	}
}

// serviceRunning checks docker compose ps JSON output for a running service.
// docker compose outputs one JSON object per line.
func serviceRunning(output []byte, serviceName string) bool {
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var container struct {
			Service string `json:"Service"`
			State   string `json:"State"`
			Name    string `json:"Name"`
		}
		if err := json.Unmarshal([]byte(line), &container); err != nil {
			continue
		}
		if container.Service == serviceName && container.State == "running" {
			return true
		}
	}
	return false
}
