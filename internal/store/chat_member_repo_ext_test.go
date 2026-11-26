// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// Chat members' store half (control-plane docs/design/chat-collaboration.md
// §3.1, PR C2).

func applyMemberChange(env *storetest.Env, c core.ChatMemberChange) (core.ShareAction, error) {
	var action core.ShareAction
	err := env.Repo.RunTx(context.Background(), orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		action, err = store.ApplyChatMemberChange(ctx, env.ORM, c)
		return err
	})
	return action, err
}

func memberChange(target string, role core.ChatMemberRole, at time.Time) core.ChatMemberChange {
	return core.ChatMemberChange{
		ChatID: "c1", OrgID: "acme", OwnerUserID: "bob", ActorUserID: "bob",
		TargetUserID: target, Role: role, At: at,
	}
}

// Every change writes exactly one share_events row, naming whom it is about
// and the level it leaves; a no-op writes none. A role change keeps when the
// person became a member and records who set the level they hold now.
func TestApplyChatMemberChange(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	shareChat(t, env, "c1", "bob", strp("acme"), base)

	steps := []struct {
		name   string
		change core.ChatMemberChange
		want   core.ShareAction
		role   core.ChatMemberRole // alice's level after it
	}{
		{"grant", memberChange("alice", core.ChatMemberView, base.Add(time.Minute)), core.ShareActionGrant, core.ChatMemberView},
		{"the level already held", memberChange("alice", core.ChatMemberView, base.Add(2*time.Minute)), "", core.ChatMemberView},
		{"role change", func() core.ChatMemberChange {
			c := memberChange("alice", core.ChatMemberManage, base.Add(3*time.Minute))
			c.ActorUserID = "max"
			return c
		}(), core.ShareActionRoleChange, core.ChatMemberManage},
		{"revoke", memberChange("alice", "", base.Add(4*time.Minute)), core.ShareActionRevoke, ""},
		{"revoke what is not held", memberChange("alice", "", base.Add(5*time.Minute)), "", ""},
	}
	wantEvents := 0
	for _, s := range steps {
		got, err := applyMemberChange(env, s.change)
		if err != nil || got != s.want {
			t.Fatalf("%s: action %q err %v, want %q", s.name, got, err, s.want)
		}
		role, err := store.ChatMemberRole(ctx, env.ORM, "c1", "alice")
		if err != nil || role != s.role {
			t.Fatalf("%s: role %q err %v, want %q", s.name, role, err, s.role)
		}
		if s.want != "" {
			wantEvents++
		}
		ev := events(t, env, core.ShareKindChat, "c1")
		if len(ev) != wantEvents {
			t.Fatalf("%s: %d share events, want %d", s.name, len(ev), wantEvents)
		}
		if s.want == "" {
			continue
		}
		last := ev[0] // newest first
		if last.Action != s.want || last.TargetUserID != "alice" || last.Role != s.change.Role ||
			last.ActorUserID != s.change.ActorUserID || last.OwnerUserID != "bob" || last.OrgID != "acme" || !last.CreatedAt.Equal(s.change.At) {
			t.Fatalf("%s: event %+v", s.name, last)
		}
		if s.name == "role change" {
			m, err := store.GetChatMember(ctx, env.ORM, "c1", "alice")
			if err != nil || m == nil || m.GrantedBy != "max" || !m.CreatedAt.Equal(base.Add(time.Minute)) {
				t.Fatalf("after a role change: %+v %v", m, err)
			}
		}
	}
}

// A change decided on a stale read (the chat's owner or org moved since)
// writes nothing.
func TestApplyChatMemberChangeIsStaleSafe(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	shareChat(t, env, "c1", "bob", strp("acme"), base)
	for name, c := range map[string]core.ChatMemberChange{
		"another owner": {ChatID: "c1", OrgID: "acme", OwnerUserID: "carol", ActorUserID: "carol", TargetUserID: "alice", Role: core.ChatMemberView, At: base},
		"another org":   {ChatID: "c1", OrgID: "globex", OwnerUserID: "bob", ActorUserID: "bob", TargetUserID: "alice", Role: core.ChatMemberView, At: base},
		"no such chat":  {ChatID: "c9", OrgID: "acme", OwnerUserID: "bob", ActorUserID: "bob", TargetUserID: "alice", Role: core.ChatMemberView, At: base},
	} {
		if got, err := applyMemberChange(env, c); err != nil || got != "" {
			t.Errorf("%s: action %q err %v, want nothing", name, got, err)
		}
	}
	if ev := events(t, env, core.ShareKindChat, "c1"); len(ev) != 0 {
		t.Fatalf("stale changes recorded %d events", len(ev))
	}
	if _, err := applyMemberChange(env, memberChange("bob", core.ChatMemberView, base)); err == nil {
		t.Fatal("the owner was made a member")
	}
}

// A chat holds at most core.MaxChatMembers members: the next grant is
// refused and writes nothing, a level change at the cap is not a new member,
// and a removal makes room.
func TestChatMembersCap(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	shareChat(t, env, "c1", "bob", strp("acme"), base)
	for i := 0; i < core.MaxChatMembers; i++ {
		if _, err := applyMemberChange(env, memberChange(fmt.Sprintf("u%02d", i), core.ChatMemberView, base)); err != nil {
			t.Fatalf("grant %d: %v", i, err)
		}
	}
	if _, err := applyMemberChange(env, memberChange("one-too-many", core.ChatMemberView, base)); !errors.Is(err, core.ErrChatMembersFull) {
		t.Fatalf("grant past the cap: %v, want ErrChatMembersFull", err)
	}
	if role, _ := store.ChatMemberRole(context.Background(), env.ORM, "c1", "one-too-many"); role != "" {
		t.Fatal("a refused grant was written")
	}
	if ev := events(t, env, core.ShareKindChat, "c1"); len(ev) != core.MaxChatMembers {
		t.Fatalf("%d events, want %d", len(ev), core.MaxChatMembers)
	}
	if got, err := applyMemberChange(env, memberChange("u00", core.ChatMemberReply, base)); err != nil || got != core.ShareActionRoleChange {
		t.Fatalf("a level change at the cap: %q %v", got, err)
	}
	if _, err := applyMemberChange(env, memberChange("u01", "", base)); err != nil {
		t.Fatal(err)
	}
	if got, err := applyMemberChange(env, memberChange("one-too-many", core.ChatMemberView, base)); err != nil || got != core.ShareActionGrant {
		t.Fatalf("a grant after a removal: %q %v", got, err)
	}
}

// Concurrent grants on one chat serialize on its row lock, so the cap cannot
// be raced past: of more grants than the cap allows, exactly the cap land.
func TestChatMembersCapUnderConcurrency(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	shareChat(t, env, "c1", "bob", strp("acme"), base)
	const n = core.MaxChatMembers + 5
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted, full := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := applyMemberChange(env, memberChange(fmt.Sprintf("u%02d", i), core.ChatMemberView, base))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				granted++
			case errors.Is(err, core.ErrChatMembersFull):
				full++
			default:
				t.Errorf("grant %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	members, err := store.ListChatMembers(context.Background(), env.ORM, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if granted != core.MaxChatMembers || full != n-core.MaxChatMembers || len(members) != core.MaxChatMembers {
		t.Fatalf("granted %d, refused %d, %d rows; want %d, %d, %d", granted, full, len(members), core.MaxChatMembers, n-core.MaxChatMembers, core.MaxChatMembers)
	}
}

func events(t *testing.T, env *storetest.Env, kind core.ShareKind, id string) []*core.ShareEvent {
	t.Helper()
	ev, err := store.ListShareEvents(context.Background(), env.ORM, kind, id)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}
