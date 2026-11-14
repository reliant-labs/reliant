// Copyright (c) 2026 Reliant Labs

package daemon_registry

import (
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// A machine whose daemon reports it cannot start processes is still ACTIVE —
// the status every client switches on keeps its meaning — and DEGRADED in the
// separate health field, with the daemon's code, reason and the gateway's
// since (reliant#1919).
func TestDaemonToProto_DegradedHealthLeavesStatusAlone(t *testing.T) {
	since := time.Date(2026, 10, 30, 17, 57, 0, 0, time.UTC)
	d := &db.Daemon{ID: "d-1", UserID: "u-1", DaemonType: strp("managed")}
	att := &db.DaemonAttachment{DaemonID: "d-1", UserID: "u-1", Health: db.MachineHealth{
		State: db.MachineHealthDegraded, Code: "process_table_full",
		Reason: "This machine cannot start new processes", Since: since,
	}}

	got := daemonToProto(d, att)
	if got.GetStatus() != reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE {
		t.Errorf("status = %v, want ACTIVE: health must not change the status enum", got.GetStatus())
	}
	want := &reliantv1.MachineHealth{
		State: reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_DEGRADED,
		Code:  "process_table_full", Reason: "This machine cannot start new processes",
		SinceMs: since.UnixMilli(),
	}
	if !proto.Equal(got.GetHealth(), want) {
		t.Errorf("health = %v, want %v", got.GetHealth(), want)
	}
}

// An attached daemon that reports no health — one that predates the report,
// or has not probed yet — reads OK; a healthy one reads OK with its since; an
// unattached daemon has no health at all (its status already says why it
// cannot work).
func TestDaemonToProto_HealthOfHealthyAndUnreportedMachines(t *testing.T) {
	since := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	d := &db.Daemon{ID: "d-1", UserID: "u-1"}

	cases := []struct {
		name string
		att  *db.DaemonAttachment
		want *reliantv1.MachineHealth
	}{
		{
			name: "attached, nothing reported",
			att:  &db.DaemonAttachment{DaemonID: "d-1", UserID: "u-1"},
			want: &reliantv1.MachineHealth{State: reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_OK},
		},
		{
			name: "attached, reported ok",
			att: &db.DaemonAttachment{DaemonID: "d-1", UserID: "u-1",
				Health: db.MachineHealth{State: db.MachineHealthOK, Since: since}},
			want: &reliantv1.MachineHealth{State: reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_OK, SinceMs: since.UnixMilli()},
		},
		{
			name: "attached, a state this server does not know",
			att: &db.DaemonAttachment{DaemonID: "d-1", UserID: "u-1",
				Health: db.MachineHealth{State: "rebooting", Code: "x", Reason: "y"}},
			want: &reliantv1.MachineHealth{State: reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_OK},
		},
		{name: "not attached", att: nil, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := daemonToProto(d, tc.att).GetHealth()
			if !proto.Equal(got, tc.want) {
				t.Errorf("health = %v, want %v", got, tc.want)
			}
		})
	}
}

// An old client — built from daemon_registry.proto before DaemonInfo had a
// health field — decodes a degraded machine's DaemonInfo exactly as before:
// the same ACTIVE status, with health an unknown field it never looks at.
func TestDaemonInfo_OldClientReadsStatusUnchanged(t *testing.T) {
	wire, err := proto.Marshal(&reliantv1.DaemonInfo{
		DaemonId: "d-1", Status: reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE,
		Health: &reliantv1.MachineHealth{State: reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_DEGRADED, Reason: "full"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The old schema: this file's descriptor with DaemonInfo.health removed.
	fdp := protodesc.ToFileDescriptorProto((&reliantv1.DaemonInfo{}).ProtoReflect().Descriptor().ParentFile())
	for _, m := range fdp.GetMessageType() {
		if m.GetName() != "DaemonInfo" {
			continue
		}
		m.Field = slices.DeleteFunc(m.Field, func(f *descriptorpb.FieldDescriptorProto) bool { return f.GetName() == "health" })
	}
	oldFile, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatal(err)
	}
	oldInfo := oldFile.Messages().ByName("DaemonInfo")
	if oldInfo.Fields().ByName("health") != nil {
		t.Fatal("the old schema still has health")
	}

	old := dynamicpb.NewMessage(oldInfo)
	if err := proto.Unmarshal(wire, old); err != nil {
		t.Fatalf("old client cannot decode: %v", err)
	}
	status := old.Get(oldInfo.Fields().ByName("status")).Enum()
	if status != protoreflect.EnumNumber(reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE) {
		t.Errorf("old client read status %v, want ACTIVE", status)
	}
	if id := old.Get(oldInfo.Fields().ByName("daemon_id")).String(); id != "d-1" {
		t.Errorf("old client read daemon_id %q, want d-1", id)
	}
}
