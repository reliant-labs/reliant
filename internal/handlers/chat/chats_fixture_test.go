package chat

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/chataccess"
	"github.com/reliant-labs/reliant/internal/chataccess/chataccesstest"
	"github.com/reliant-labs/reliant/internal/chatupdates"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/questions"
	"github.com/reliant-labs/reliant/internal/store"
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

// A fixture that names neither ChatRows nor ChatViews reads them through its
// own repository: the generated stores over a *db.Repo's ORM, or a repository
// with its own GetChat and GetChatRow (a hand fixture, or a MockRepository
// that programs them). A test that needs another answer names the role itself,
// which always wins.
func init() {
	fixtureChatReads = func(repo Repository) (ChatRows, ChatViews) {
		if client := ormOf(repo); client != nil {
			return store.NewChatExtStore(client), store.NewChatsWithActivityExtStore(client)
		}
		if v, ok := boundChatReads.Load(repo); ok {
			b := v.(*boundReads)
			return b, b
		}
		reads := &MockChatReads{}
		return reads, reads
	}
}

// A fixture that builds a Service without naming Deps.TriggerReads reads a
// chat's launch event through its own repository: the generated trigger event
// store over a *db.Repo's ORM, or the repository itself when it answers the
// read (a hand fixture). A test that needs another answer names the role.
func init() {
	fixtureTriggerReads = func(repo Repository, reads TriggerReads) TriggerReads {
		if mock, ok := reads.(*MockTriggerReads); reads != nil && (!ok || mock.GetTriggerEventByChatIDFunc != nil) {
			return reads
		}
		if client := ormOf(repo); client != nil {
			return store.NewTriggerEventExtStore(client)
		}
		if r, ok := any(repo).(TriggerReads); ok {
			return r
		}
		return &MockTriggerReads{}
	}
}

// boundChatReads maps a mock repository to the chat reads its fixture
// programmed (the role of the old GetChatFunc / GetChatRowFunc on the mock).
var boundChatReads sync.Map

type boundReads struct {
	row  func(context.Context, string) (*db.ChatRow, error)
	view func(context.Context, string) (*db.Chat, error)
}

func (b *boundReads) GetChatRow(ctx context.Context, id string) (*db.ChatRow, error) {
	if b.row == nil {
		return nil, fmt.Errorf("chat fixture: GetChatRow not programmed")
	}
	return b.row(ctx, id)
}

func (b *boundReads) GetChat(ctx context.Context, id string) (*db.Chat, error) {
	if b.view == nil {
		return nil, fmt.Errorf("chat fixture: GetChat not programmed")
	}
	return b.view(ctx, id)
}

// BindChatReads programs the chat reads Deps.ChatRows and Deps.ChatViews
// answer for a fixture built over repo, and returns repo.
func BindChatReads[R any](repo R, view func(context.Context, string) (*db.Chat, error), row func(context.Context, string) (*db.ChatRow, error)) R {
	boundChatReads.Store(any(repo), &boundReads{row: row, view: view})
	return repo
}

// MockChatReads answers every read as not programmed, so an unprogrammed fixture
// is loud.
type MockChatReads struct{}

func (*MockChatReads) GetChatRow(context.Context, string) (*db.ChatRow, error) {
	return nil, fmt.Errorf("chat fixture: GetChatRow not programmed")
}

func (*MockChatReads) GetChat(context.Context, string) (*db.Chat, error) {
	return nil, fmt.Errorf("chat fixture: GetChat not programmed")
}

// ormOf finds the database handle of a fixture repository: its own ORM(), or
// that of a *db.Repo it embeds (a wrapper that fails or counts one call).
func ormOf(repo any) *orm.Client {
	if r, ok := repo.(interface{ ORM() *orm.Client }); ok && r.ORM() != nil {
		return r.ORM()
	}
	v := reflect.ValueOf(repo)
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if !f.Anonymous || !v.Field(i).CanInterface() {
			continue
		}
		if c := ormOf(v.Field(i).Interface()); c != nil {
			return c
		}
	}
	return nil
}

// A fixture that names no QuestionResolver resolves through its own repository
// (a *db.Repo's ORM, as in production); one over a mock repository fails
// loudly instead.
func init() {
	fixtureQuestionResolver = func(repo Repository) QuestionResolver {
		if client := ormOf(repo); client != nil {
			return questions.New(store.NewQuestionExtStore(client), activityOf(repo), updatesOf(repo))
		}
		return &MockQuestionResolver{}
	}
}

type noActivity struct{}

func (noActivity) EmitChatActivityIfChanged(context.Context, string) error { return nil }

// activityOf is the repository's chat-activity announcer, or none.
func activityOf(repo any) questions.ChatActivity {
	if a, ok := repo.(questions.ChatActivity); ok {
		return a
	}
	return noActivity{}
}

type noUpdates struct{}

func (noUpdates) EmitQuestionUpdate(context.Context, string, core.QuestionUpdate) error { return nil }

// updatesOf is the repository's chat-update writer, or none.
func updatesOf(repo any) questions.Updates {
	if r, ok := repo.(interface{ UpdateWriter() chatupdates.Service }); ok {
		return r.UpdateWriter()
	}
	return noUpdates{}
}
