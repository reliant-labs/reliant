package chat

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// requireRepositoryLacks fails when Repository declares method. The handlers
// reach the database only through Deps.Repo, and every callee they hand it to
// takes a role Repository embeds, so a method Repository does not declare is
// one no chat rpc can call: the compiler pins what a spy counting zero calls
// used to.
func requireRepositoryLacks(t *testing.T, method, why string) {
	t.Helper()
	_, declared := reflect.TypeFor[Repository]().MethodByName(method)
	require.False(t, declared, "chat.Repository declares %s: %s", method, why)
}
