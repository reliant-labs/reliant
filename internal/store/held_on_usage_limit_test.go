package store_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// Only a chat with a thread parked on a usage-limit wait right now, begun
// after the cutoff, counts as held: not a driver's retry ladder, not a wait
// that was cleared, not one older than the bound.
func TestListChatsHeldOnUsageLimit(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for _, c := range []string{"held", "ladder", "cleared", "old", "other"} {
		seedChatThread(t, env, c, "th-"+c)
	}
	if err := store.RecordUsageLimitWait(ctx, env.ORM, "held", "th-held", "Anthropic", core.ReasonUsageLimit, now.Add(2*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordProviderBackoff(ctx, env.ORM, "ladder", "th-ladder", 1, 5, 429, "http_429", time.Minute, now); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUsageLimitWait(ctx, env.ORM, "cleared", "th-cleared", "Anthropic", core.ReasonUsageLimitProbe, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClearUsageLimitWait(ctx, env.ORM, "cleared", "th-cleared", now); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUsageLimitWait(ctx, env.ORM, "old", "th-old", "Anthropic", core.ReasonUsageLimit, now.Add(time.Hour), now.Add(-7*time.Hour)); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListChatsHeldOnUsageLimit(ctx, env.ORM, []string{"held", "ladder", "cleared", "old", "other", "no-such-chat"}, now.Add(-6*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"held"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("held chats = %v, want %v", got, want)
	}
	if got, err := store.ListChatsHeldOnUsageLimit(ctx, env.ORM, []string{"other"}, now.Add(-6*time.Hour)); err != nil || len(got) != 0 {
		t.Fatalf("a chat outside the held set = %v, %v", got, err)
	}
}
