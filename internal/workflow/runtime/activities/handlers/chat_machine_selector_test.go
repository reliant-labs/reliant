package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatDaemonSelector(t *testing.T) {
	pin := "original"
	chat := &db.Chat{ActiveDaemonID: &pin}
	t.Run("live turn uses durable pin", func(t *testing.T) {
		sel := chatDaemonSelector(chat, nil)
		require.NotNil(t, sel)
		assert.Equal(t, pin, sel.ID)
	})
	t.Run("explicit workflow and node routes are unchanged", func(t *testing.T) {
		for _, explicit := range []*types.DaemonSelector{
			{ID: "cicd"}, {Name: "cicd"}, {Type: "cloud"}, {Labels: map[string]string{"role": "ci"}},
		} {
			assert.Same(t, explicit, chatDaemonSelector(chat, explicit))
		}
	})
	t.Run("thread checkout still beats inherited chat pin", func(t *testing.T) {
		sel := toolDaemonSelector("thread-owner", threadBoundExplicit(true, "thread-owner", chatDaemonSelector(chat, nil)))
		require.NotNil(t, sel)
		assert.Equal(t, "thread-owner", sel.ID)
	})
	// A run launched before its chat was moved to the workspace's machine
	// (worktree_owner_backfill, SetChatDaemon) carries the old pin as an
	// explicit id selector for its whole life. The checkout is on the owner
	// alone, so tools and workspace repair go there, never to the old pin.
	t.Run("a stale id pin to another machine yields to the workspace owner", func(t *testing.T) {
		moved := "owner"
		migrated := &db.Chat{ActiveDaemonID: &moved}
		oldRun := &types.DaemonSelector{ID: "former-pin", Session: true} // the run's session_daemon_id
		sel := toolDaemonSelector("owner", chatDaemonSelector(migrated, oldRun))
		require.NotNil(t, sel)
		assert.Equal(t, "owner", sel.ID)
		// The pin read from the chat at activity time is a session pin too.
		stale := "former-pin"
		sel = toolDaemonSelector("owner", chatDaemonSelector(&db.Chat{ActiveDaemonID: &stale}, nil))
		require.NotNil(t, sel)
		assert.Equal(t, "owner", sel.ID)
	})
	t.Run("deliberate node routing keeps precedence over the workspace owner", func(t *testing.T) {
		for _, explicit := range []*types.DaemonSelector{
			{ID: "cicd"}, {Name: "cicd"}, {Type: "cloud"}, {Labels: map[string]string{"role": "ci"}}, {ID: "owner", Labels: map[string]string{"gpu": "1"}},
		} {
			sel := toolDaemonSelector("owner", explicit)
			require.NotNil(t, sel)
			assert.Equal(t, explicit.ID, sel.ID, "a configured id routes where it says, even off the workspace owner")
			assert.Equal(t, explicit.Name, sel.Name)
			assert.Equal(t, explicit.Type, sel.Type)
			assert.Equal(t, explicit.Labels, sel.Labels)
		}
		// No workspace owner (the main checkout): an id pin is the route.
		sel := toolDaemonSelector("", &types.DaemonSelector{ID: "former-pin", Session: true})
		require.NotNil(t, sel)
		assert.Equal(t, "former-pin", sel.ID)
	})
	t.Run("unbound and no-machine chats do not acquire routes", func(t *testing.T) {
		assert.Nil(t, chatDaemonSelector(nil, nil))
		assert.Nil(t, chatDaemonSelector(&db.Chat{}, nil))
		empty := ""
		assert.Nil(t, chatDaemonSelector(&db.Chat{ActiveDaemonID: &empty}, nil))
		assert.Nil(t, chatDaemonSelector(&db.Chat{NoMachine: true, ActiveDaemonID: &pin}, nil))
	})
}
