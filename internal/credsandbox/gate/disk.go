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
	"strings"
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
//
// A slot is wiped only when nothing can still be using it. The process-local
// lock says no live process holds the slot, but containers outlive the
// process that started them, so ownership is also persisted beside the slot,
// where its lock file is (DiskSlotsDir, which this process can write even
// when the engine is failing; the slot's own root belongs to its filesystem):
//
//   - slot-<n>.owner names the run the slot was last claimed for. It is
//     written before any of that run's containers exist and cleared only
//     after Teardown confirmed them removed and wiped the slot. A claim that
//     finds one first confirms through the engine that no container labelled
//     with that run id exists; if it cannot confirm it, the slot is skipped,
//     never wiped.
//   - slot-<n>.quarantine marks a slot whose run's containers Teardown could
//     not list or remove. No claim takes it. A later Teardown of the same run
//     that confirms them gone wipes the slot and clears the marker; otherwise
//     an operator does, after checking the slot is unused.

// slotName is a disk slot's directory name inside DiskSlotsDir.
var slotName = regexp.MustCompile(`^slot-[0-9]+$`)

// errSlotBusy: another gate holds the slot.
var errSlotBusy = errors.New("disk slot in use")

// errNoFreeSlot: no slot can be claimed now; the error says why for each.
var errNoFreeSlot = errors.New("no disk slot is free")

// diskSlot is a slot a gate holds: its directory and its lock.
type diskSlot struct {
	dir  string
	lock *os.File
}

func ownerPath(slot string) string      { return slot + ".owner" }
func quarantinePath(slot string) string { return slot + ".quarantine" }

// readOwner returns the run id slot was last claimed for ("" when none). An
// unreadable record, or one that is not a run id, is an error: it must never
// become a label filter.
func readOwner(slot string) (string, error) {
	b, err := os.ReadFile(ownerPath(slot))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if !runIDPattern.MatchString(id) {
		return "", fmt.Errorf("%s holds %q, which is not a run id", ownerPath(slot), id)
	}
	return id, nil
}

// writeSlotFile replaces path's content durably: a crash leaves the old
// content or the new, never a torn one.
func writeSlotFile(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func removeSlotFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// slotUnusable says why slot, which the caller has locked, must not be
// claimed ("" when it may be): it is quarantined, or a previous owner's
// containers may survive.
func (s *Sysbox) slotUnusable(ctx context.Context, slot string) string {
	name := filepath.Base(slot)
	if b, err := os.ReadFile(quarantinePath(slot)); err == nil {
		return fmt.Sprintf("%s is quarantined (%s)", name, strings.TrimSpace(string(b)))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf("%s's quarantine marker is unreadable: %v", name, err)
	}
	owner, err := readOwner(slot)
	if err != nil {
		return fmt.Sprintf("%s's owner record is unreadable: %v", name, err)
	}
	if owner == "" {
		return ""
	}
	out, err := s.output(ctx, nil, "ps", "-aq", "--filter", "label="+LabelRunID+"="+owner)
	if err != nil {
		return fmt.Sprintf("%s: cannot confirm its previous run %s has no containers left: %v", name, owner, err)
	}
	if ids := strings.Fields(out); len(ids) > 0 {
		return fmt.Sprintf("%s: its previous run %s still has containers (%s)", name, owner, strings.Join(ids, " "))
	}
	return ""
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
// limit, records runID as its owner, and wipes it for the gate. A slot that
// is locked, quarantined, or whose previous run may still have containers is
// skipped and left as it is.
func (s *Sysbox) claimSlot(ctx context.Context, runID string, limit int64) (*diskSlot, error) {
	dir := s.cfg.DiskSlotsDir
	slots, err := listSlots(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDiskLimitNotEnforced, err)
	}
	var skipped []string
	for _, p := range slots {
		lock, err := lockSlot(p + ".lock")
		if errors.Is(err, errSlotBusy) {
			skipped = append(skipped, filepath.Base(p)+" is in use")
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("locking disk slot %s: %w", p, err)
		}
		if why := s.slotUnusable(ctx, p); why != "" {
			_ = lock.Close()
			skipped = append(skipped, why)
			continue
		}
		size, err := s.slotSize(dir, p)
		if err == nil && size > limit {
			err = fmt.Errorf("disk slot %s holds %d bytes, more than the gate's disk limit of %d", p, size, limit)
		}
		if err == nil {
			// Before any of the run's containers exist, so a crash from
			// here on leaves a record the next claim reconciles.
			if werr := writeSlotFile(ownerPath(p), runID+"\n"); werr != nil {
				err = fmt.Errorf("recording disk slot %s's owner: %w", p, werr)
			}
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
	return nil, fmt.Errorf("%w in %s: %s", errNoFreeSlot, dir, strings.Join(skipped, "; "))
}

// releaseSlot releases the slot runID holds: in this process, or, after a
// restart, the unlocked slot whose owner record names runID. containersErr
// is why Teardown could not confirm runID's containers removed (nil when it
// did). Only then is the slot wiped, its records cleared and its lock
// dropped; otherwise it is quarantined and, when this process holds it,
// kept locked so a retried Teardown can still release it.
func (s *Sysbox) releaseSlot(ctx context.Context, runID string, containersErr error) error {
	s.mu.Lock()
	slot := s.slots[runID]
	delete(s.slots, runID)
	s.mu.Unlock()
	if slot == nil {
		if slot = s.findOwnedSlot(runID); slot == nil {
			return nil
		}
	}
	if containersErr != nil {
		s.mu.Lock()
		s.slots[runID] = slot
		s.mu.Unlock()
		reason := fmt.Sprintf("run %s: its containers could not be confirmed removed: %v", runID, containersErr)
		if err := writeSlotFile(quarantinePath(slot.dir), reason+"\n"); err != nil {
			return fmt.Errorf("%w: disk slot %s is kept locked, but its quarantine marker could not be written (%v): %s", ErrDiskSlotQuarantined, slot.dir, err, reason)
		}
		return fmt.Errorf("%w: disk slot %s was not wiped or released, and %s marks why: %s", ErrDiskSlotQuarantined, slot.dir, quarantinePath(slot.dir), reason)
	}
	defer func() { _ = slot.lock.Close() }()
	if err := s.wipeSlot(ctx, runID, slot.dir); err != nil {
		// The owner record stays: no container is left, so the next claim
		// confirms that and wipes the slot itself.
		return err
	}
	if err := removeSlotFile(quarantinePath(slot.dir)); err != nil {
		return fmt.Errorf("clearing disk slot %s's quarantine marker: %w", slot.dir, err)
	}
	return removeSlotFile(ownerPath(slot.dir))
}

// findOwnedSlot locks the slot whose owner record names runID, when no one
// holds it (nil when there is none, or it is held).
func (s *Sysbox) findOwnedSlot(runID string) *diskSlot {
	if s.cfg.DiskSlotsDir == "" {
		return nil
	}
	slots, err := listSlots(s.cfg.DiskSlotsDir)
	if err != nil {
		return nil
	}
	for _, p := range slots {
		if owner, err := readOwner(p); err != nil || owner != runID {
			continue
		}
		lock, err := lockSlot(p + ".lock")
		if err != nil {
			return nil
		}
		// Re-read under the lock: a claim may have taken it meanwhile.
		if owner, err := readOwner(p); err != nil || owner != runID {
			_ = lock.Close()
			return nil
		}
		return &diskSlot{dir: p, lock: lock}
	}
	return nil
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
