// Copyright (c) 2025 Reliant Labs
package analytics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

type EventBatch struct {
	Events    []Event   `json:"events"`
	CreatedAt time.Time `json:"createdAt"`
	Retries   int       `json:"retries"`
}

func LoadFailedEvents() []EventBatch {
	dataDir, err := getAnalyticsDataDir()
	if err != nil {
		logging.Warn("[Statsig] Failed to get analytics directory", "error", err)
		return nil
	}

	files, err := filepath.Glob(filepath.Join(dataDir, "failed_*.json"))
	if err != nil {
		logging.Warn("[Statsig] Failed to list failed event files", "error", err)
		return nil
	}

	var batches []EventBatch
	cutoff := time.Now().Add(-30 * 24 * time.Hour)

	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			if err := os.Remove(file); err != nil {
				logging.Warn("[Statsig] Failed to remove old failed event file", "file", file, "error", err)
			}
			continue
		}

		// #nosec G304 -- file path comes from filepath.Glob constrained to analytics data dir
		data, err := os.ReadFile(file)
		if err != nil {
			logging.Warn("[Statsig] Failed to read failed event file", "file", file, "error", err)
			continue
		}

		var events []Event
		if err := json.Unmarshal(data, &events); err != nil {
			logging.Warn("[Statsig] Failed to unmarshal failed events", "file", file, "error", err)
			if err := os.Remove(file); err != nil {
				logging.Warn("[Statsig] Failed to remove corrupted failed event file", "file", file, "error", err)
			}
			continue
		}

		batches = append(batches, EventBatch{
			Events:    events,
			CreatedAt: info.ModTime(),
		})

		if err := os.Remove(file); err != nil {
			logging.Warn("[Statsig] Failed to remove processed failed event file", "file", file, "error", err)
		}
	}

	return batches
}

func RetryFailedEvents(client *Client) {
	batches := LoadFailedEvents()
	if len(batches) == 0 {
		return
	}

	for _, batch := range batches {
		payload := map[string]interface{}{
			"events": batch.Events,
		}

		if err := client.sendEvents(payload); err != nil {
			logging.Warn("[Statsig] Failed to retry event batch", "error", err, "eventCount", len(batch.Events))

			if batch.Retries < maxRetries {
				batch.Retries++
				client.saveFailedEvents(batch.Events)
			}
		}
	}
}
