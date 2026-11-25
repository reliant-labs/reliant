// Copyright (c) 2026 Reliant Labs

package daemon_registry

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// An attached daemon's stored resource reading reaches DaemonInfo.resources
// as the heartbeat carried it. Nothing stored, a value that does not decode,
// and an unattached daemon all leave it unset rather than failing the list.
func TestDaemonToProto_CarriesTheResourceReading(t *testing.T) {
	rss, fds := uint64(300<<20), int64(64)
	stats := &reliantv1.DaemonResourceStats{
		Machine: &reliantv1.MachineStats{Cpus: 8, MemTotalBytes: 32 << 30, MemAvailableBytes: 20 << 30},
		Process: &reliantv1.DaemonProcessStats{RssBytes: &rss, OpenFds: &fds, Goroutines: 200},
	}
	stored, err := protojson.Marshal(stats)
	if err != nil {
		t.Fatal(err)
	}
	// A newer gateway's field this one does not know is ignored.
	withUnknown := `{"machine":{"cpus":4},"fromTheFuture":1}`

	d := &db.Daemon{ID: "d-1", UserID: "u-1"}
	cases := []struct {
		name string
		att  *db.DaemonAttachment
		want *reliantv1.DaemonResourceStats
	}{
		{"stored", &db.DaemonAttachment{DaemonID: "d-1", ResourceStats: string(stored)}, stats},
		{"unknown field", &db.DaemonAttachment{DaemonID: "d-1", ResourceStats: withUnknown},
			&reliantv1.DaemonResourceStats{Machine: &reliantv1.MachineStats{Cpus: 4}}},
		{"none reported", &db.DaemonAttachment{DaemonID: "d-1"}, nil},
		{"damaged", &db.DaemonAttachment{DaemonID: "d-1", ResourceStats: "{not json"}, nil},
		{"not attached", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := daemonToProto(d, tc.att).GetResources()
			if !proto.Equal(got, tc.want) {
				t.Errorf("resources = %v, want %v", got, tc.want)
			}
		})
	}
}
