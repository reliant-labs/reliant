// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemoninstance"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// daemonRecordStatus is what a daemon's runtime record says about the process
// that wrote it, before anything is said about its gateway stream.
type daemonRecordStatus int

const (
	// daemonRecordMissing: no runtime record — no daemon ran here.
	daemonRecordMissing daemonRecordStatus = iota
	// daemonRecordStale: a record whose PID is gone.
	daemonRecordStale
	// daemonRecordLive: a record whose PID is alive.
	daemonRecordLive
)

// readDaemonRecord reads dataDir's runtime record and whether its process is
// alive. An error is a record that exists but cannot be read; a missing record
// is daemonRecordMissing, not an error. `reliant daemon status` and
// localSandboxState share it, so they can never disagree on "is it running".
func readDaemonRecord(dataDir string) (daemonstate.State, daemonRecordStatus, error) {
	state, err := daemonstate.Read(dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return state, daemonRecordMissing, nil
		}
		return state, daemonRecordMissing, err
	}
	if !daemonProcessAlive(state.PID) {
		return state, daemonRecordStale, nil
	}
	return state, daemonRecordLive, nil
}

// daemonConnected reports whether dataDir holds a live daemon whose gateway
// stream is established.
func daemonConnected(dataDir string) bool {
	state, status, err := readDaemonRecord(dataDir)
	return err == nil && status == daemonRecordLive && state.Stream.Established()
}

// sandboxState is how far this machine is from serving agents for an account.
type sandboxState string

const (
	// sandboxNone: no daemon credential for the account on this machine.
	sandboxNone sandboxState = "none"
	// sandboxPaired: a daemon credential exists, but no daemon of the
	// account is connected right now.
	sandboxPaired sandboxState = "paired"
	// sandboxConnected: a daemon of the account runs here with its stream up.
	sandboxConnected sandboxState = "connected"
)

// localSandboxState reports whether this machine is an agent sandbox of
// account on server: paired is a stored daemon credential, connected is a
// daemon of that account (any workspace instance) running here with an
// established gateway stream. account "" is the server's default credential,
// for logins that predate account ids.
//
// Cheap and offline: it reads two local files per instance, never dials, and
// never fails — anything unreadable counts as "not there".
func localSandboxState(server, account string) sandboxState {
	creds, err := auth.ReadDaemonCredentials(server, account)
	if err != nil || creds == nil {
		return sandboxNone
	}
	root, err := daemoninstance.InstancesDir()
	if err != nil {
		return sandboxPaired
	}
	sub := strings.TrimSpace(creds.Sub)
	if sub == auth.DefaultAccount {
		sub = ""
	}
	key := daemoninstance.Key{Origin: daemoninstance.OriginKey(server), Sub: sub}
	if key.Origin == "" {
		return sandboxPaired
	}
	workspaces, err := os.ReadDir(filepath.Join(root, key.OriginSlug(), key.SubSlug()))
	if err != nil {
		return sandboxPaired
	}
	for _, w := range workspaces {
		if w.IsDir() && daemonConnected(filepath.Join(root, key.OriginSlug(), key.SubSlug(), w.Name())) {
			return sandboxConnected
		}
	}
	return sandboxPaired
}
