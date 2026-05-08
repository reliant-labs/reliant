package toolexec

import (
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

type recordingLifecycleNATSConn struct {
	messages []*nats.Msg
	err      error
}

func (c *recordingLifecycleNATSConn) PublishMsg(msg *nats.Msg) error {
	if c.err != nil {
		return c.err
	}
	c.messages = append(c.messages, msg)
	return nil
}

func TestDaemonLifecyclePublisherPublishesConnectedEvent(t *testing.T) {
	conn := &recordingLifecycleNATSConn{}
	publisher := newDaemonLifecyclePublisher(conn)

	publisher.OnDaemonConnected("user-1", "daemon-1")

	require.Len(t, conn.messages, 1)
	require.Equal(t, "daemon.v1.events.connected", conn.messages[0].Subject)

	var evt daemonLifecycleEvent
	require.NoError(t, json.Unmarshal(conn.messages[0].Data, &evt))
	require.Equal(t, daemonEventVersion, evt.Version)
	require.Equal(t, "connected", evt.Type)
	require.Equal(t, "user-1", evt.UserID)
	require.Equal(t, "daemon-1", evt.DaemonID)
	require.False(t, evt.Timestamp.IsZero())
}

func TestDaemonLifecyclePublisherPublishesDisconnectedEvent(t *testing.T) {
	conn := &recordingLifecycleNATSConn{}
	publisher := newDaemonLifecyclePublisher(conn)

	publisher.OnDaemonDisconnected("user-1", "daemon-1")

	require.Len(t, conn.messages, 1)
	require.Equal(t, "daemon.v1.events.disconnected", conn.messages[0].Subject)
}

func TestDaemonLifecyclePublisherSkipsMissingIdentity(t *testing.T) {
	conn := &recordingLifecycleNATSConn{}
	publisher := newDaemonLifecyclePublisher(conn)

	publisher.OnDaemonConnected("", "daemon-1")
	publisher.OnDaemonDisconnected("user-1", "")

	require.Empty(t, conn.messages)
}
