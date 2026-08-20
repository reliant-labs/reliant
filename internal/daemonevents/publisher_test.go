package daemonevents

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// fakeJetStream implements jetstream.JetStream by embedding the interface
// (nil) and overriding only Publish, the single method Publisher.publish
// calls. Any other method would panic on the nil embedded value, which is
// fine — these tests never exercise them.
type fakeJetStream struct {
	jetstream.JetStream
	published []publishedMsg
}

type publishedMsg struct {
	subject string
	data    []byte
}

func (f *fakeJetStream) Publish(_ context.Context, subject string, payload []byte, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	f.published = append(f.published, publishedMsg{subject: subject, data: payload})
	return &jetstream.PubAck{}, nil
}

// control-plane's mirror of Event, hand-copied here so the test can prove
// the JSON one side emits actually decodes into the shape the other side
// expects. The two structs are hand-synced across repos with nothing else
// enforcing that; this is the property that matters.
type controlPlaneDaemonEvent struct {
	Version   int       `json:"version"`
	Type      string    `json:"type"`
	UserID    string    `json:"userId"`
	DaemonID  string    `json:"daemonId"`
	Timestamp time.Time `json:"timestamp"`
	Reason    string    `json:"reason,omitempty"`
	Name      string    `json:"name,omitempty"`
	Hostname  string    `json:"hostname,omitempty"`
	Platform  string    `json:"platform,omitempty"`
}

func TestPublisher_OnDaemonConnectedWithInfo_MarshalsNameHostnamePlatform(t *testing.T) {
	js := &fakeJetStream{}
	p := &Publisher{js: js}

	p.OnDaemonConnectedWithInfo("user-1", "daemon-1", "my-laptop", "my-laptop.local", "darwin")

	require.Len(t, js.published, 1)
	require.Equal(t, SubjectConnected, js.published[0].subject)

	var evt Event
	require.NoError(t, json.Unmarshal(js.published[0].data, &evt))
	require.Equal(t, CurrentEventVersion, evt.Version)
	require.Equal(t, EventTypeConnected, evt.Type)
	require.Equal(t, "user-1", evt.UserID)
	require.Equal(t, "daemon-1", evt.DaemonID)
	require.Equal(t, "my-laptop", evt.Name)
	require.Equal(t, "my-laptop.local", evt.Hostname)
	require.Equal(t, "darwin", evt.Platform)

	// Round-trip against control-plane's independently-maintained struct:
	// this is the property that actually matters, since nothing but this
	// test enforces the two stay in sync.
	var cpEvt controlPlaneDaemonEvent
	require.NoError(t, json.Unmarshal(js.published[0].data, &cpEvt))
	require.Equal(t, evt.Name, cpEvt.Name)
	require.Equal(t, evt.Hostname, cpEvt.Hostname)
	require.Equal(t, evt.Platform, cpEvt.Platform)
}

func TestPublisher_NamePrecedence(t *testing.T) {
	tests := []struct {
		name         string
		daemonName   string
		hostname     string
		daemonID     string
		wantResolved string
	}{
		{
			name:         "name wins when present",
			daemonName:   "my-laptop",
			hostname:     "my-laptop.local",
			daemonID:     "daemon-1",
			wantResolved: "my-laptop",
		},
		{
			name:         "falls back to hostname when name empty",
			daemonName:   "",
			hostname:     "my-laptop.local",
			daemonID:     "daemon-1",
			wantResolved: "my-laptop.local",
		},
		{
			name:         "falls back to daemon id when both empty",
			daemonName:   "",
			hostname:     "",
			daemonID:     "daemon-1",
			wantResolved: "daemon-1",
		},
		{
			name:         "whitespace-only name treated as empty",
			daemonName:   "   ",
			hostname:     "my-laptop.local",
			daemonID:     "daemon-1",
			wantResolved: "my-laptop.local",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			js := &fakeJetStream{}
			p := &Publisher{js: js}

			p.OnDaemonConnectedWithInfo("user-1", tt.daemonID, tt.daemonName, tt.hostname, "darwin")

			require.Len(t, js.published, 1)
			var evt Event
			require.NoError(t, json.Unmarshal(js.published[0].data, &evt))
			require.Equal(t, tt.wantResolved, evt.Name)
		})
	}
}

func TestPublisher_OnDaemonConnected_NoInfoStillPublishes(t *testing.T) {
	js := &fakeJetStream{}
	p := &Publisher{js: js}

	p.OnDaemonConnected("user-1", "daemon-1")

	require.Len(t, js.published, 1)
	var evt Event
	require.NoError(t, json.Unmarshal(js.published[0].data, &evt))
	// No name/hostname available on this path, so the resolved name falls
	// all the way back to the daemon id.
	require.Equal(t, "daemon-1", evt.Name)
	require.Empty(t, evt.Hostname)
}
