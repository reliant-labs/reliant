// Copyright (c) 2025 Reliant Labs
package tools

import (
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// fileAwarenessKey combines chat ID, thread path and file path for chat+thread-scoped tracking
type fileAwarenessKey struct {
	chatID   string
	thread   string
	filePath string
}

var (
	// Track when AI last became "aware" of file contents (via read or write)
	fileAwarenessTime      = make(map[fileAwarenessKey]time.Time)
	fileAwarenessTimeMutex sync.RWMutex
)

// recordFileAwareness records that the AI is now aware of the file's current contents
// This happens when the AI reads the file (View tool) or writes to it (Edit tool)
func recordFileAwareness(chatID, thread, filePath string) {
	fileAwarenessTimeMutex.Lock()
	defer fileAwarenessTimeMutex.Unlock()

	key := fileAwarenessKey{
		chatID:   chatID,
		thread:   thread,
		filePath: filePath,
	}
	fileAwarenessTime[key] = time.Now()
	logging.Debug("recordFileAwareness", "chatID", chatID, "thread", thread, "filePath", filePath)
}

// getLastAwarenessTime returns the last time the AI was aware of the file's contents
func getLastAwarenessTime(chatID, thread, filePath string) time.Time {
	fileAwarenessTimeMutex.RLock()
	defer fileAwarenessTimeMutex.RUnlock()

	key := fileAwarenessKey{
		chatID:   chatID,
		thread:   thread,
		filePath: filePath,
	}
	result := fileAwarenessTime[key] // Returns zero time if not found
	logging.Debug("getLastAwarenessTime", "chatID", chatID, "thread", thread, "filePath", filePath, "found", !result.IsZero(), "timestamp", result.Format(time.RFC3339))
	return result
}

// ClearFileRecordsForThread clears all file records for a specific chat+thread (for testing)
func ClearFileRecordsForThread(chatID, thread string) {
	fileAwarenessTimeMutex.Lock()
	defer fileAwarenessTimeMutex.Unlock()

	for key := range fileAwarenessTime {
		if key.chatID == chatID && key.thread == thread {
			delete(fileAwarenessTime, key)
		}
	}
}
