// Copyright (c) 2025 Reliant Labs

//go:build linux

package gate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestDiskSlotBoundsWritesForReal runs the disk-slot path against the real
// engine (plain runc, no sysbox needed): a gate's work volume on a slot
// cannot be written past the slot's size, and teardown wipes what was
// written. It needs a provisioned slot directory, as gate-check makes one:
//
//	truncate -s 64M slot-0.img && mkfs.ext4 -q slot-0.img
//	mkdir -p slots/slot-0 && sudo mount -o loop slot-0.img slots/slot-0
//	RELIANT_GATE_TEST_DISK_SLOTS=$PWD/slots go test -run TestDiskSlotBoundsWritesForReal ./internal/credsandbox/gate/
//
// RELIANT_GATE_TEST_HELPER_IMAGE (default busybox:1.36) is the wipe helper
// and the writer.
func TestDiskSlotBoundsWritesForReal(t *testing.T) {
	testkit.Slow(t, "runs containers on the real engine against a mounted disk slot")
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
