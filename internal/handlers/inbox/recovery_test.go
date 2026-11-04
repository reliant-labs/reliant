package inbox

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestRecoveryInboxSurvivesSourceDisappearance(t *testing.T) {
	row := &core.WorktreeRecovery{ID: "catalog", SourceWorktreeID: "deleted-worktree", ProjectID: "deleted-project", DaemonID: "deleted-machine", Phase: "held", Operation: "restoring", OperationKey: "retry-key", TargetDaemonID: "target", CreatedAt: time.Now(), Checkouts: []core.RecoveryCheckout{{RepoID: "one", Phase: "restored"}, {RepoID: "two", Phase: "held", LastError: "remote unavailable"}}}
	item := recoveryInboxItem(row)
	if item.ItemId != "recovery:catalog" || item.GetRecovery().RecoveryId != "catalog" || item.GetRecovery().CheckoutCount != 2 || !strings.Contains(item.GetRecovery().LastError, "two: remote unavailable") {
		t.Fatal("retained identity/partial held outcome lost")
	}
	if got := item.GetRecovery(); got.Operation != "restoring" || got.OperationKey != "retry-key" || got.TargetDaemonId != "target" {
		t.Fatal("durable operation retry identity lost on inbox reload")
	}
	if inboxItemBlocks(item) {
		t.Fatal("informational recovery blocks runs")
	}
	if err := validateInboxItemIDs([]string{item.ItemId}); err != nil {
		t.Fatal("cannot dismiss retained recovery", err)
	}
}
