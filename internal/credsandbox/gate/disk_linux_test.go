// Copyright (c) 2025 Reliant Labs

//go:build linux && integration

package gate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDiskSlotBoundsWritesForReal runs the disk-slot path against the real
// engine (plain runc, no sysbox needed): a gate's work volume on a slot
// cannot be written past the slot's size, and teardown wipes what was
// written. It needs a provisioned slot directory, as gate-check makes one:
//
//	truncate -s 64M slot-0.img && mkfs.ext4 -q slot-0.img
//	mkdir -p slots/slot-0 && sudo mount -o loop slot-0.img slots/slot-0
//	RELIANT_GATE_TEST_DISK_SLOTS=$PWD/slots go test -tags integration -run ForReal ./internal/credsandbox/gate/
//
// RELIANT_GATE_TEST_HELPER_IMAGE (default busybox:1.36) is the wipe helper
// and the writer.
func TestDiskSlotBoundsWritesForReal(t *testing.T) {
	dir := os.Getenv("RELIANT_GATE_TEST_DISK_SLOTS")
	if dir == "" {
		t.Skip("RELIANT_GATE_TEST_DISK_SLOTS names no provisioned disk slot directory")
	}
	image := os.Getenv("RELIANT_GATE_TEST_HELPER_IMAGE")
	if image == "" {
		image = "busybox:1.36"
	}
	s := NewSysbox(SysboxConfig{ProbeImage: image, DiskSlotsDir: dir})
	ctx := context.Background()
	slots, err := listSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	size, err := slotSize(dir, slots[0])
	if err != nil {
		t.Fatal(err)
	}
	spec := validSpec()
	spec.RunID = "disk-" + randomSuffix()
	spec.Limits.DiskBytes = size
	n := namesFor(spec.RunID)
	t.Cleanup(func() { _ = s.Teardown(context.WithoutCancel(ctx), spec.RunID) })
	if err := s.createVolumes(ctx, spec, n); err != nil {
		t.Fatalf("createVolumes: %v", err)
	}
	mb := size>>20 + 16
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--runtime", "runc", "--network", "none",
		"--label", LabelRunID+"="+spec.RunID, "--mount", "type=volume,src="+n.work+",dst=/w", image,
		"sh", "-c", "dd if=/dev/zero of=/w/fill bs=1M count="+strconv.FormatInt(mb, 10)).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "No space left") {
		t.Fatalf("writing %d MiB onto a %d-byte slot: %v\n%s", mb, size, err, out)
	}
	fi, err := os.Stat(filepath.Join(slots[0], "work", "fill"))
	if err != nil || fi.Size() > size {
		t.Fatalf("the write did not land on the slot, or exceeded it: %v %v", fi, err)
	}
	t.Logf("a %d-byte slot stopped a %d MiB write at %d bytes", size, mb, fi.Size())
	if err := s.Teardown(ctx, spec.RunID); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if _, err := os.Stat(filepath.Join(slots[0], "work", "fill")); !os.IsNotExist(err) {
		t.Errorf("teardown left the gate's data on the slot: %v", err)
	}
	// Freed: the next gate claims it again.
	again := spec
	again.RunID = "disk-" + randomSuffix()
	t.Cleanup(func() { _ = s.Teardown(context.WithoutCancel(ctx), again.RunID) })
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := s.createVolumes(cctx, again, namesFor(again.RunID)); err != nil {
		t.Errorf("claiming the freed slot: %v", err)
	}
}

// TestDiskSlotSurvivesARestartForReal: after a daemon restart (a fresh
// backend, the slot unlocked) a gate container still running on the slot
// keeps it from being claimed or wiped, through the real engine; once that
// container is gone, the stale owner's slot is reclaimed. Same setup as
// TestDiskSlotBoundsWritesForReal.
func TestDiskSlotSurvivesARestartForReal(t *testing.T) {
	dir := os.Getenv("RELIANT_GATE_TEST_DISK_SLOTS")
	if dir == "" {
		t.Skip("RELIANT_GATE_TEST_DISK_SLOTS names no provisioned disk slot directory")
	}
	image := os.Getenv("RELIANT_GATE_TEST_HELPER_IMAGE")
	if image == "" {
		image = "busybox:1.36"
	}
	ctx := context.Background()
	slots, err := listSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 1 {
		t.Skipf("needs exactly one slot in %s, so a refused claim cannot fall through to another", dir)
	}
	size, err := slotSize(dir, slots[0])
	if err != nil {
		t.Fatal(err)
	}
	cfg := SysboxConfig{ProbeImage: image, DiskSlotsDir: dir}
	first := NewSysbox(cfg)
	spec := validSpec()
	spec.RunID = "disk-" + randomSuffix()
	spec.Limits.DiskBytes = size
	n := namesFor(spec.RunID)
	t.Cleanup(func() { _ = NewSysbox(cfg).Teardown(context.WithoutCancel(ctx), spec.RunID) })
	if err := first.createVolumes(ctx, spec, n); err != nil {
		t.Fatalf("createVolumes: %v", err)
	}
	// The gate: running on the slot, labelled with its run id.
	if out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--runtime", "runc", "--network", "none",
		"--label", LabelRunID+"="+spec.RunID, "--mount", "type=volume,src="+n.work+",dst=/w", image,
		"sh", "-c", "echo gate-data >/w/data && exec sleep 3600").CombinedOutput(); err != nil {
		t.Fatalf("starting the gate: %v\n%s", err, out)
	}
	data := filepath.Join(slots[0], "work", "data")
	if !pollUntil(30*time.Second, func() bool { _, err := os.Stat(data); return err == nil }) {
		t.Fatal("the gate wrote nothing to the slot")
	}
	crash(first) // the daemon dies: its lock goes, the gate and the owner record stay

	restarted := NewSysbox(cfg)
	other := spec
	other.RunID = "disk-" + randomSuffix()
	t.Cleanup(func() { _ = NewSysbox(cfg).Teardown(context.WithoutCancel(ctx), other.RunID) })
	err = restarted.createVolumes(ctx, other, namesFor(other.RunID))
	if !errors.Is(err, errNoFreeSlot) || !strings.Contains(err.Error(), "still has containers") {
		t.Fatalf("claim with the previous gate running = %v", err)
	}
	if _, err := os.Stat(data); err != nil {
		t.Fatalf("the running gate's data was wiped: %v", err)
	}
	// The gate is gone (removed out of band, as an operator would): the
	// stale owner's slot is reclaimed and wiped.
	ids, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label="+LabelRunID+"="+spec.RunID).Output()
	if err != nil || len(strings.Fields(string(ids))) != 1 {
		t.Fatalf("finding the gate: %q %v", ids, err)
	}
	if out, err := exec.CommandContext(ctx, "docker", append([]string{"rm", "-f", "-v"}, strings.Fields(string(ids))...)...).CombinedOutput(); err != nil {
		t.Fatalf("removing the gate: %v\n%s", err, out)
	}
	if err := restarted.createVolumes(ctx, other, namesFor(other.RunID)); err != nil {
		t.Fatalf("claim of the stale owner's slot: %v", err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Errorf("the reclaimed slot was not wiped: %v", err)
	}
	if owner, _ := readOwner(slots[0]); owner != other.RunID {
		t.Errorf("the reclaimed slot's owner = %q, want %q", owner, other.RunID)
	}
}
