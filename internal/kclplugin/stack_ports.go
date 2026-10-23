package kclplugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/devstack"
)

// UseStackPortStore coordinates all checkout/env reservations at the repository
// anchor. Scope uses the exact project directory (not a basename or branch).
// No rollback: a failed launch may reserve a port but must never erase another
// process's concurrent allocation. Reservations are stable across retries.
func UseStackPortStore(projectDir, env string, readOnly bool) {
	path := filepath.Join(devstack.SharedProjectDir(projectDir), ".forge", "host-ports.json")
	r := NewPersistentPortResolver(path)
	r.readOnly = readOnly
	r.scope = filepath.Clean(projectDir) + ":" + env
	armResolver(r)
}

// reserveFixed publishes deterministic block ports into the SAME reservation
// transaction as free ports. Fixed ports cannot move (a cluster may pre-map
// them), so a conflicting prior reservation fails plainly rather than adopting
// or silently moving a listener. Later resolve_port calls step around them.
func (r *PortResolver) reserveFixed(base int, key string, port int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.storePath == "" || r.readOnly {
		return nil
	}
	name := fmt.Sprintf("block:%s:%d", key, base)
	return devstack.WithFileLock(r.storePath+".lock", func() error {
		ports := map[string]int{}
		data, err := os.ReadFile(r.storePath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			if err := json.Unmarshal(data, &ports); err != nil {
				return err
			}
		}
		for owner, p := range ports {
			if p == port && owner != name {
				return fmt.Errorf("allocate_port(%d, %q): port %d reserved by %s; change the conflicting fixed port declaration (reservations remain stable across down/up)", base, key, port, owner)
			}
		}
		ports[name] = port
		return writePorts(r.storePath, ports)
	})
}

// UseStackPortStoreReadOnly arms ordinary renders without replacing a writable
// launch context. The shared store is also the read side's source of truth.
func UseStackPortStoreReadOnly(projectDir, env string) {
	resolverMu.Lock()
	defer resolverMu.Unlock()
	if defaultResolver.Bound() && (!defaultResolver.readOnly || defaultResolver.scope == filepath.Clean(projectDir)+":"+env) {
		return
	}
	path := filepath.Join(devstack.SharedProjectDir(projectDir), ".forge", "host-ports.json")
	r := NewReadOnlyPortResolver(path)
	r.scope = filepath.Clean(projectDir) + ":" + env
	defaultResolver = r
}
