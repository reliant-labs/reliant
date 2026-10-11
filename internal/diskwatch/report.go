// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// The server half: the gateway stores a heartbeat's report in
// daemons.storage_state, and the registry and the storage inbox item read it
// back. It is stored on the daemon row, not on the attachment row with the
// memory telemetry, because a full disk is still full after a disconnect.

// Encode stamps the server's clock on a report and serializes it for storage.
func Encode(rep *reliantv1.DiskReport, now time.Time) (string, error) {
	cp := proto.Clone(rep).(*reliantv1.DiskReport)
	cp.ReportedAtMs = now.UnixMilli()
	b, err := protojson.Marshal(cp)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Decode reads a stored report. Nil when there is none, it is undecodable, or
// it has no volumes — which includes the shape stored before reports existed
// (one worktree-root reading, path/free_bytes/total_bytes), deliberately: that
// reading measured the wrong volume, and showing it is how the UI said "fine"
// while the cache volume was full.
func Decode(stored string) *reliantv1.DiskReport {
	if strings.TrimSpace(stored) == "" {
		return nil
	}
	var rep reliantv1.DiskReport
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(stored), &rep); err != nil {
		return nil
	}
	if len(rep.Volumes) == 0 {
		return nil
	}
	return &rep
}

// Signature changes when a report differs materially from the last one
// stored: a volume's pressure, a 1%-of-the-volume step in free space or
// inodes, a fresh consumer measurement, a no-space failure, or a clean-up
// starting or finishing. The gateway writes only when it changes (or when the
// stored report is old), not on every 15s heartbeat.
func Signature(rep *reliantv1.DiskReport) string {
	if rep == nil {
		return ""
	}
	var b strings.Builder
	for _, v := range rep.Volumes {
		step := max(v.TotalBytes/100, 1)
		fstep := max(v.FilesTotal/100, 1)
		fmt.Fprintf(&b, "%s|%d|%d|%d|%s;", UrgentVolumeSignature(v), v.TotalBytes, v.FreeBytes/step, v.FilesFree/fstep, strings.Join(v.Paths, ","))
	}
	return b.String()
}

// UrgentSignature changes only when the UI should hear about it now — a
// pressure flip, new consumers, a no-space failure, a clean-up starting or
// ending — as opposed to free space drifting.
func UrgentSignature(rep *reliantv1.DiskReport) string {
	if rep == nil {
		return ""
	}
	var b strings.Builder
	for _, v := range rep.Volumes {
		b.WriteString(UrgentVolumeSignature(v))
		b.WriteByte(';')
	}
	return b.String()
}

// UrgentVolumeSignature is one volume's part of UrgentSignature.
func UrgentVolumeSignature(v *reliantv1.DiskVolume) string {
	var clearing []string
	for _, c := range v.Consumers {
		if c.Clearing {
			clearing = append(clearing, c.Kind)
		}
	}
	sort.Strings(clearing)
	return fmt.Sprintf("%s|%t|%d|%d|%s", v.Label, v.Pressure, v.ConsumersMeasuredAtMs, v.LastNoSpaceAtMs, strings.Join(clearing, ","))
}

// UnderPressure returns the report's volumes under pressure, fullest first.
func UnderPressure(rep *reliantv1.DiskReport) []*reliantv1.DiskVolume {
	if rep == nil {
		return nil
	}
	var out []*reliantv1.DiskVolume
	for _, v := range rep.Volumes {
		if v.Pressure {
			out = append(out, v)
		}
	}
	SortFullestFirst(out)
	return out
}

// SortFullestFirst orders volumes by the fraction of space used, highest
// first.
func SortFullestFirst(vols []*reliantv1.DiskVolume) {
	used := func(v *reliantv1.DiskVolume) float64 {
		if v.TotalBytes == 0 {
			return 0
		}
		return 1 - float64(v.FreeBytes)/float64(v.TotalBytes)
	}
	sort.SliceStable(vols, func(i, j int) bool { return used(vols[i]) > used(vols[j]) })
}
