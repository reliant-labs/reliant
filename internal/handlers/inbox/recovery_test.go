package inbox

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestRecoveryInboxSurvivesSourceDisappearance(t *testing.T) {
	row := &core.WorktreeRecovery{ID: "catalog", SourceWorktreeID: "deleted-worktree", ProjectID: "deleted-project", DaemonID: "deleted-machine", Phase: "held", CreatedAt: time.Now(), Checkouts: []core.RecoveryCheckout{{RepoID: "one", Phase: "restored"}, {RepoID: "two", Phase: "held", LastError: "remote unavailable"}}}
	item := recoveryInboxItem(row)
	if item.ItemId != "recovery:catalog" || item.GetRecovery().RecoveryId != "catalog" || item.GetRecovery().CheckoutCount != 2 || !strings.Contains(item.GetRecovery().LastError, "two: remote unavailable") {
		t.Fatal("retained identity/partial held outcome lost")
	}
	if inboxItemBlocks(item) {
		t.Fatal("informational recovery blocks runs")
	}
	if err := validateInboxItemIDs([]string{item.ItemId}); err != nil {
		t.Fatal("cannot dismiss retained recovery", err)
	}
}
