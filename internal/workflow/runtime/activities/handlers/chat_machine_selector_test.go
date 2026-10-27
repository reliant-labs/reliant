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
	t.Run("unbound and no-machine chats do not acquire routes", func(t *testing.T) {
		assert.Nil(t, chatDaemonSelector(nil, nil))
		assert.Nil(t, chatDaemonSelector(&db.Chat{}, nil))
		empty := ""
		assert.Nil(t, chatDaemonSelector(&db.Chat{ActiveDaemonID: &empty}, nil))
		assert.Nil(t, chatDaemonSelector(&db.Chat{NoMachine: true, ActiveDaemonID: &pin}, nil))
	})
}
