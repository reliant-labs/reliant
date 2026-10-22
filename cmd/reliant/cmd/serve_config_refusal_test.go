// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The api-server is the process that migrates the schema (migrate=apply), so a
// setting it cannot serve with must stop it before it touches the database:
// the config's buf.validate rules refuse the value as `server api` loads its
// config (proto/config/v1/api_server_config.proto), before runRoot binds the
// health port or applies a migration. Checked in the constructor that reads
// it, the same value would fail the process only after it had migrated.
//
// The binary path end to end: Execute, as main runs it, exits 1, and the
// database it was pointed at never saw a connection.
func TestServerAPIRefusesBadConfigBeforeMigrating(t *testing.T) {
	isolateServerRoleEnv(t)
	database := newDialCounter(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	run := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExecuteAsHelperProcess$", "--", executeHelperMarker, "server", "api")
	run.Env = environWith(map[string]string{
		"DATABASE_URL": "postgres://reliant@" + database.addr + "/reliant?sslmode=disable",
		// A regression must fail fast, not wait out the default connect
		// budget, and must not need the default health port to be free.
		"DB_CONNECT_TIMEOUT": "2s",
		"HEALTH_PORT":        fmt.Sprint(freeTCPPort(t)),
		// apikey mode without its key: the message-level rule's case.
		"AUTH_MODE": "apikey",
	}, "AUTH_API_KEY")
	out, err := run.CombinedOutput()

	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), "`reliant server api` must exit non-zero; err=%v, output:\n%s", err, out)
	assert.Equal(t, 1, exit.ExitCode(), "output:\n%s", out)
	assert.Contains(t, string(out), "AUTH_MODE=apikey requires AUTH_API_KEY")
	assert.Zero(t, database.dials.Load(), "the api-server connected to its database before refusing its config; output:\n%s", out)
}

const executeHelperMarker = "reliant-execute"

// TestExecuteAsHelperProcess is not a test: re-executed by a test above, it
// runs Execute — main's whole command line — on the arguments after the
// marker, and exits as the binary would.
func TestExecuteAsHelperProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) && os.Args[i+1] == executeHelperMarker {
			os.Args = append([]string{ServiceName}, os.Args[i+2:]...)
			Execute()
			os.Exit(0)
		}
	}
}

// dialCounter is a TCP address that counts the connections made to it and
// closes each at once: a database nothing should reach.
type dialCounter struct {
	addr  string
	dials atomic.Int32
}

func newDialCounter(t *testing.T) *dialCounter {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	d := &dialCounter{addr: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			d.dials.Add(1)
			_ = conn.Close()
		}
	}()
	return d
}

// freeTCPPort is a loopback port nothing listens on now.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// environWith is this process's environment with set applied and unset
// removed.
func environWith(set map[string]string, unset ...string) []string {
	drop := map[string]bool{}
	for _, k := range unset {
		drop[k] = true
	}
	for k := range set {
		drop[k] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !drop[k] {
			env = append(env, kv)
		}
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}
	return env
}
