// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
)

// backgroundJournalDirName is the journal's directory under the data dir.
const backgroundJournalDirName = "background-processes"

// installBackgroundJournal restores the background-process records the last
// daemon on dataDir journaled, then journals this daemon's. Without it a
// restart forgot every process: shell_wait answered "No process with ID" and
// the run's verdict and output were gone. A journal that cannot be read is
// reported and skipped; it never stops the daemon.
func installBackgroundJournal(ctx context.Context, dataDir string) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return
	}
	journal := shell.NewBackgroundJournal(filepath.Join(dataDir, backgroundJournalDirName))
	mgr := shell.GetBackgroundManager()
	restored, err := journal.Restore(mgr, time.Now())
	if err != nil {
		logging.WarnContext(ctx, logPrefix+" Background process journal unavailable; processes will not survive a restart", "error", err)
		return
	}
	if restored > 0 {
		logging.InfoContext(ctx, logPrefix+" Restored background process records", "count", restored)
	}
	mgr.SetPersistenceCallback(journal.Callback())
}
