// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/daemonquery"
	"github.com/reliant-labs/reliant/internal/db"
)

func onlineTestNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	var srv *server.Server = natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	require.True(t, srv.ReadyForConnections(2*time.Second))
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// The user has daemon A up and daemon B parked. The answer depends on which
// daemon is asked about, not on whether any is up.
func TestIsDaemonOnline_PerDaemon(t *testing.T) {
	records := func(attached ...string) *fakeDaemonRecords {
		return &fakeDaemonRecords{
			daemons: []*db.Daemon{
				{ID: "daemon-a", UserID: "u"},
				{ID: "daemon-b", UserID: "u", LifecyclePhase: strPtr("suspended")},
			},
			attached: attached,
		}
	}
	check := func(t *testing.T, r *NATSDaemonRouter) {
		ctx := context.Background()
		any, err := r.IsDaemonOnline(ctx, "u", nil)
		require.NoError(t, err)
		assert.True(t, any, "nil selector: any daemon")
		for sel, want := range map[string]bool{"daemon-a": true, "daemon-b": false, "missing": false} {
			got, err := r.IsDaemonOnline(ctx, "u", &DaemonSelector{ID: sel})
			require.NoError(t, err)
			assert.Equal(t, want, got, sel)
		}
	}

	t.Run("db fallback", func(t *testing.T) {
		check(t, NewNATSDaemonRouter(nil, WithDatabase(records("daemon-a"))))
	})

	t.Run("nats status responder", func(t *testing.T) {
		nc := onlineTestNATS(t)
		_, err := nc.Subscribe(daemonquery.SubjectStatus("daemon-a"), func(m *nats.Msg) {
			body, _ := json.Marshal(daemonquery.Status{Connected: true, LastActiveMs: time.Now().UnixMilli()})
			_ = m.Respond(body)
		})
		require.NoError(t, err)
		_, err = nc.Subscribe(daemonquery.SubjectUserAnyLive("u"), func(m *nats.Msg) {
			body, _ := json.Marshal(daemonquery.UserLiveness{Live: true, Count: 1})
			_ = m.Respond(body)
		})
		require.NoError(t, err)
		require.NoError(t, nc.Flush())
		// No DB attachment rows: the NATS answer alone decides.
		check(t, NewNATSDaemonRouter(nc, WithDatabase(records())))
	})
}
