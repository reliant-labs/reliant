// Copyright (c) 2025 Reliant Labs
package gate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

// The disk limit fails closed: a gate whose disk cannot be bounded is
// refused (ErrDiskLimitNotEnforced), never run unbounded. A machine bounds it
// one of two ways:
//
//   - xfs with project quotas under the engine's volumes directory: each of
//     the gate's two volumes is created with size=Limits.DiskBytes, which the
//     engine's local driver enforces. ext4, and xfs without prjquota, refuse
//     the option.
//   - disk slots (SysboxConfig.DiskSlotsDir): filesystems the machine
//     provides, one per concurrent gate, for instance loop-backed images of a
//     fixed size that gate-check (pipeline build plan E9c) creates and mounts.
//     A gate claims a free slot under a lock, the slot is wiped, and the
//     gate's volumes are bind volumes of directories on it, so the slot's size
//     bounds everything the gate writes there. A slot larger than the gate's
//     limit is refused: it would not enforce the limit asked for.
//
// The container's own writable layer is outside both (the gate's HOME, TMPDIR,
// tree and nested engine all live on the volumes).

// slotName is a disk slot's directory name inside DiskSlotsDir.
var slotName = regexp.MustCompile(`^slot-[0-9]+$`)

// errSlotBusy: another gate holds the slot.
var errSlotBusy = errors.New("disk slot in use")

// diskSlot is a slot a gate holds: its directory and its lock.
type diskSlot struct {
	dir  string
	lock *os.File
}

// createVolumes creates the gate's work and engine volumes with its disk
// limit enforced, or refuses.
func (s *Sysbox) createVolumes(ctx context.Context, spec Spec, n names) error {
	vols := []struct{ name, role string }{{n.work, "work"}, {n.engine, "engine"}}
	if s.cfg.DiskSlotsDir == "" {
		for _, v := range vols {
			if err := s.createVolume(ctx, spec.RunID, v.name, v.role, spec.Limits.DiskBytes); err != nil {
				return fmt.Errorf("%w: the %s volume cannot take a size limit (the engine's volumes need xfs with project quotas, or the machine must provide disk slots): %v", ErrDiskLimitNotEnforced, v.role, err)
			}
		}
		return nil
	}
	slot, err := s.claimSlot(ctx, spec.RunID, spec.Limits.DiskBytes)
	if err != nil {
		return err
	}
	for _, v := range vols {
		args := []string{"volume", "create", "--driver", "local"}
		args = append(args, labelArgs(spec.RunID, v.role)...)
		args = append(args, "--opt", "type=none", "--opt", "o=bind", "--opt", "device="+filepath.Join(slot.dir, v.role), v.name)
		if _, err := s.output(ctx, nil, args...); err != nil {
			return err
		}
	}
	return nil
}

// claimSlot takes the first free disk slot, checks it bounds the gate to
// limit, and wipes it for the gate.
func (s *Sysbox) claimSlot(ctx context.Context, runID string, limit int64) (*diskSlot, error) {
	dir := s.cfg.DiskSlotsDir
	slots, err := listSlots(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDiskLimitNotEnforced, err)
	}
	for _, p := range slots {
		lock, err := lockSlot(p + ".lock")
		if errors.Is(err, errSlotBusy) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("locking disk slot %s: %w", p, err)
		}
		size, err := s.slotSize(dir, p)
		if err == nil && size > limit {
			err = fmt.Errorf("disk slot %s holds %d bytes, more than the gate's disk limit of %d", p, size, limit)
		}
		if err == nil {
			err = s.wipeSlot(ctx, runID, p)
		}
		if err != nil {
			_ = lock.Close()
			return nil, fmt.Errorf("%w: %v", ErrDiskLimitNotEnforced, err)
		}
		slot := &diskSlot{dir: p, lock: lock}
		s.mu.Lock()
		s.slots[runID] = slot
		s.mu.Unlock()
		return slot, nil
	}
	return nil, fmt.Errorf("all %d disk slots in %s are in use", len(slots), dir)
}

// releaseSlot wipes and unlocks the slot runID holds, if it holds one. A
// slot whose holder died unlocks with it and is wiped by the next claim.
func (s *Sysbox) releaseSlot(ctx context.Context, runID string) error {
	s.mu.Lock()
	slot := s.slots[runID]
	delete(s.slots, runID)
	s.mu.Unlock()
	if slot == nil {
		return nil
	}
	defer func() { _ = slot.lock.Close() }()
	return s.wipeSlot(ctx, runID, slot.dir)
}

// wipeScript empties a slot and leaves the gate's two volume directories on
// it. lost+found belongs to the filesystem.
const wipeScript = `set -eu
find /slot -mindepth 1 -maxdepth 1 ! -name lost+found -exec rm -rf {} +
mkdir /slot/work /slot/engine
left=$(find /slot -mindepth 1 -maxdepth 1 ! -name lost+found ! -name work ! -name engine)
[ -z "$left" ]
`

// wipeSlot empties a slot through the engine: what a gate wrote there is
// owned by its root, which only root on the host may remove. The helper is
// the machine's trusted ProbeImage, without network, with the slot alone
// mounted.
func (s *Sysbox) wipeSlot(ctx context.Context, runID, dir string) error {
	if s.cfg.ProbeImage == "" {
		return errors.New("no probe image is configured, so a disk slot cannot be wiped")
	}
	args := []string{"run", "--rm", "--runtime", "runc", "--network", "none",
		"--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER",
		"--security-opt", "no-new-privileges", "--user", "0"}
	args = append(args, labelArgs(runID, "wipe")...)
	args = append(args, "--mount", "type=bind,src="+dir+",dst=/slot", "--entrypoint", "/bin/sh", s.cfg.ProbeImage, "-c", wipeScript)
	if _, err := s.output(ctx, nil, args...); err != nil {
		return fmt.Errorf("wiping disk slot %s: %w", dir, err)
	}
	return nil
}

// diskPreflight reports whether this machine can enforce a gate's disk limit.
func (s *Sysbox) diskPreflight(ctx context.Context, probeID string) (bool, string) {
	if dir := s.cfg.DiskSlotsDir; dir != "" {
		slots, err := listSlots(dir)
		if err != nil {
			return false, err.Error()
		}
		var sizes []string
		for _, p := range slots {
			size, err := s.slotSize(dir, p)
			if err != nil {
				return false, err.Error()
			}
			sizes = append(sizes, filepath.Base(p)+"="+strconv.FormatInt(size, 10))
		}
		return true, fmt.Sprintf("%d disk slots in %s (bytes: %v); a gate's disk limit must be at least its slot's size", len(slots), dir, sizes)
	}
	vol := volumeName(probeID, "probe")
	if err := s.createVolume(ctx, probeID, vol, "probe", 1<<20); err != nil {
		return false, fmt.Sprintf("the engine cannot size-limit volumes (it needs the local driver on xfs with project quotas), and no disk slots are configured: %v", err)
	}
	return true, "volumes take a size limit"
}

// listSlots lists the slot-<n> directories of dir, in order.
func listSlots(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading the disk slots directory: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && slotName.MatchString(e.Name()) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s holds no slot-<n> directories", dir)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(filepath.Base(out[i])[len("slot-"):])
		b, _ := strconv.Atoi(filepath.Base(out[j])[len("slot-"):])
		return a < b
	})
	return out, nil
}
