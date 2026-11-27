package devstack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// hostPortsRel is the machine-shared host-port reservation store
// (internal/kclplugin's PortResolver writes it). It holds two kinds of entry:
//
//   - "block:<key>:<base>" — the fixed port allocate_port(base, key) handed
//     out from key's block, reserved so no other declaration takes it;
//   - "<checkout dir>:<env>:<name>" — a resolve_port reservation, scoped to
//     the exact checkout and env that made it.
//
// Both outlive the block registry entry or the checkout they came from unless
// something drops them, and the "block:" kind blocks the NEXT key that is
// given the freed block: its fixed ports collide with the dead key's
// reservations, and `forge env up` fails "port N reserved by block:<dead>:…".
const hostPortsRel = ".forge/host-ports.json"

// HostPortsPath is the shared host-port reservation store for projectDir: in
// the primary checkout's counterpart of projectDir (SharedProjectDir), so every
// worktree of one repo reserves against the same file. Its writers serialize
// on HostPortsPath(projectDir)+".lock".
func HostPortsPath(projectDir string) string {
	return filepath.Join(SharedProjectDir(projectDir), filepath.FromSlash(hostPortsRel))
}

// blockReservation reports whether name is a "block:<key>:<base>" reservation
// of exactly key. Keys are canonical DNS labels (validateKey), so they never
// contain ':' and the split is unambiguous.
func blockReservation(name, key string) bool {
	rest, ok := strings.CutPrefix(name, "block:")
	if !ok {
		return false
	}
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return false
	}
	if _, err := strconv.Atoi(rest[i+1:]); err != nil {
		return false
	}
	return rest[:i] == key
}

// dropHostPortReservations removes every reservation in projectDir's shared
// host-port store that drop selects, under the store's own writer lock, and
// returns the removed names, sorted. With dryRun it only reports them. A
// missing store has nothing to drop.
func dropHostPortReservations(projectDir string, dryRun bool, drop func(name string) bool) ([]string, error) {
	path := HostPortsPath(projectDir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil // never create the store (or its lock) just to find it empty
	}
	var dropped []string
	err := WithFileLock(path+".lock", func() error {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read host-port reservations: %w", err)
		}
		ports := map[string]int{}
		if err := json.Unmarshal(data, &ports); err != nil {
			return fmt.Errorf("parse host-port reservations %s: %w", path, err)
		}
		for name := range ports {
			if drop(name) {
				dropped = append(dropped, name)
			}
		}
		if dryRun || len(dropped) == 0 {
			return nil
		}
		for _, name := range dropped {
			delete(ports, name)
		}
		out, err := json.MarshalIndent(ports, "", "  ")
		if err != nil {
			return err
		}
		return atomicfile.WriteFile(path, out, 0o600)
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(dropped)
	return dropped, nil
}
