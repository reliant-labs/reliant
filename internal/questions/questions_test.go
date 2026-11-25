package questions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

type fakeStore struct {
	inserted   []*db.Question
	resolved   []string
	byID       map[string]*db.Question
	insertErr  error
	resolveErr error
	getErr     error
}

func (f *fakeStore) InsertQuestion(_ context.Context, q *db.Question) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inserted = append(f.inserted, q)
	return nil
}

func (f *fakeStore) GetQuestion(_ context.Context, id string) (*db.Question, error) {
	return f.byID[id], f.getErr
}

func (f *fakeStore) ResolveQuestion(_ context.Context, id string, _ *string, _ time.Time) error {
	if f.resolveErr != nil {
		return f.resolveErr
	}
	f.resolved = append(f.resolved, id)
	return nil
}

type fakeActivity struct{ chats []string }

func (a *fakeActivity) EmitChatActivityIfChanged(_ context.Context, chatID string) error {
	a.chats = append(a.chats, chatID)
	return errors.New("announce failed") // never fails the write
}

// A created question announces its chat's activity exactly once, and an
// announcement that fails does not fail the write.
func TestCreateQuestion_AnnouncesTheChatOnce(t *testing.T) {
	t.Parallel()
	st, act := &fakeStore{}, &fakeActivity{}
	err := New(st, act).CreateQuestion(context.Background(), &db.Question{ID: "q1", ChatID: "chat-1"})
	require.NoError(t, err)
	assert.Len(t, st.inserted, 1)
	assert.Equal(t, []string{"chat-1"}, act.chats)
}

// A write that fails announces nothing: the activity has not changed.
func TestCreateQuestion_AFailedWriteAnnouncesNothing(t *testing.T) {
	t.Parallel()
	act := &fakeActivity{}
	err := New(&fakeStore{insertErr: errors.New("boom")}, act).CreateQuestion(context.Background(), &db.Question{ID: "q1", ChatID: "chat-1"})
	require.Error(t, err)
	assert.Empty(t, act.chats)
}

func TestCreateQuestion_RefusesAnEmptyOrNilQuestion(t *testing.T) {
	t.Parallel()
	svc := New(&fakeStore{}, &fakeActivity{})
	require.Error(t, svc.CreateQuestion(context.Background(), nil))
	require.Error(t, svc.CreateQuestion(context.Background(), &db.Question{ChatID: "c"}))
}

// A resolved question announces the chat it belongs to.
func TestResolveQuestion_AnnouncesTheQuestionsChat(t *testing.T) {
	t.Parallel()
	st := &fakeStore{byID: map[string]*db.Question{"q1": {ID: "q1", ChatID: "chat-1"}}}
	act := &fakeActivity{}
	require.NoError(t, New(st, act).ResolveQuestion(context.Background(), "q1", nil))
	assert.Equal(t, []string{"q1"}, st.resolved)
	assert.Equal(t, []string{"chat-1"}, act.chats)
}

// The resolve is the write; a failed read-back or announcement after it does
// not fail it, and a failed resolve announces nothing.
func TestResolveQuestion_OnlyTheResolveCanFail(t *testing.T) {
	t.Parallel()
	act := &fakeActivity{}
	require.NoError(t, New(&fakeStore{getErr: errors.New("read failed")}, act).ResolveQuestion(context.Background(), "q1", nil))
	assert.Empty(t, act.chats, "no chat known, nothing announced")

	act = &fakeActivity{}
	require.Error(t, New(&fakeStore{resolveErr: errors.New("boom")}, act).ResolveQuestion(context.Background(), "q1", nil))
	assert.Empty(t, act.chats)

	require.Error(t, New(&fakeStore{}, &fakeActivity{}).ResolveQuestion(context.Background(), "", nil))
}
