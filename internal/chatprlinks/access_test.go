// Copyright (c) 2025 Reliant Labs
package chatprlinks_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/chatprlinks"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// TestListForChatIsTheOwnersAlone: which pull requests a chat produced is
// the input to what it cost, so only its owner reads its links. A viewer of
// the chat its owner shared with the org, even on a read site that serves
// viewers, an outsider, and the owner through a credential bound to another
// org are all answered as for a chat that does not exist.
func TestListForChatIsTheOwnersAlone(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc, err := chatprlinks.New(chatprlinks.Deps{
		Store: store.NewChatPrLinkExtStore(repo.ORM()),
		Chats: repo,
		Now:   func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	pr := core.PRRef{Provider: "github", Repository: "acme/widgets", Number: 7}
	if n, err := svc.LinkRunPR(context.Background(), f.ChatID, "run-1", pr); err != nil || n != 1 {
		t.Fatalf("LinkRunPR = %d, %v", n, err)
	}

	links, err := svc.ListForChat(f.Owner, f.OwnerID, f.ChatID)
	if err != nil || len(links) != 1 || links[0].PRRef != pr || links[0].RunID != "run-1" || links[0].Source != core.ChatPRLinkIntegration {
		t.Fatalf("the owner's read = %+v, %v", links, err)
	}

	for _, tc := range []struct {
		name   string
		ctx    context.Context
		userID string
	}{
		{"viewer", f.Viewer, f.ViewerID},
		{"viewer on a read site that serves viewers", authz.WithViewerReads(f.Viewer), f.ViewerID},
		{"admin of the org", authz.WithViewerReads(f.Admin), f.AdminID},
		{"outsider", f.Outsider, f.OutsiderID},
		{"outsider on a read site that serves viewers", authz.WithViewerReads(f.Outsider), f.OutsiderID},
		{"owner through another org's credential", authz.WithCredentialOrg(f.Owner, viewertest.OtherOrgID), f.OwnerID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.ListForChat(tc.ctx, tc.userID, f.ChatID)
			if !errors.Is(err, core.ErrChatNotFound) || got != nil {
				t.Fatalf("ListForChat = %+v, %v; want core.ErrChatNotFound and nothing", got, err)
			}
		})
	}
	if _, err := svc.ListForChat(f.Owner, f.OwnerID, "no-such-chat"); !errors.Is(err, core.ErrChatNotFound) {
		t.Fatalf("a missing chat = %v, want core.ErrChatNotFound", err)
	}
}
