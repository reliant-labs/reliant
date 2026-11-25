package chat

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/chataccess"
	"github.com/reliant-labs/reliant/internal/chataccess/chataccesstest"
)

// A fixture that builds a Service without naming Deps.Chats reads chats
// through its own repository: the real chataccess service over a *db.Repo's
// ORM, or the reads a fixture bound to a mock (chataccesstest.Bind). A test
// that needs another answer names Chats itself, which always wins.
func init() {
	fixtureChats = func(repo Repository) chataccess.Service { return chataccesstest.For(repo) }
}

// The fixture default above is a test convenience only: a production Deps
// without Chats is refused.
func TestValidateDepsRequiresChats(t *testing.T) {
	t.Parallel()
	deps := newTestChatDeps(defaultTestConfig(t))
	require.NoError(t, deps.validateDeps(), "the harness's deps are complete")
	deps.Chats = nil
	err := deps.validateDeps()
	require.ErrorContains(t, err, "Deps.Chats is required")
}
