package cli

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchForChanges watches the proto/ directory for changes and re-runs the
// generate pipeline on each change. Uses fsnotify with a debounce to coalesce
// rapid successive writes (e.g. an editor writing + renaming).
func watchForChanges() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	// Recursively add all directories under proto/
	if err := addWatchDirs(watcher, "proto"); err != nil {
		return fmt.Errorf("failed to watch proto/: %w", err)
	}

	// Debounce timer — wait 500ms after the last event before regenerating
	var debounce *time.Timer
	const debounceDelay = 500 * time.Millisecond

	// Track whether additional changes arrived while a regen is in-flight
	// so we don't lose events during the (potentially lengthy) regen.
	var pendingMu sync.Mutex
	var pending bool
	var lastEvent string

	// Listen for OS interrupt to exit cleanly
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			// Only care about .proto file changes
			if !strings.HasSuffix(event.Name, ".proto") {
				// But if a new directory is created, watch it too
				if event.Has(fsnotify.Create) {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						_ = watcher.Add(event.Name)
					}
				}
				continue
			}

			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Remove) {
				pendingMu.Lock()
				pending = true
				lastEvent = event.Name
				pendingMu.Unlock()

				// Reset debounce timer
				if debounce != nil {
					debounce.Stop()
				}
				debounce = time.AfterFunc(debounceDelay, func() {
					for {
						pendingMu.Lock()
						if !pending {
							pendingMu.Unlock()
							break
						}
						pending = false
						eventName := lastEvent
						pendingMu.Unlock()

						fmt.Printf("\n🔄 Change detected: %s\n", eventName)
						if err := withGenerateLock(".", func() error {
							return runGeneratePipeline(".", false)
						}); err != nil {
							log.Printf("Generation failed: %v", err)
						}
					}
					fmt.Println("\n👀 Watching for changes... (Press Ctrl+C to stop)")
				})
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			log.Printf("Watcher error: %v", err)

		case <-sigCh:
			fmt.Println("\nStopping watch mode...")
			return nil
		}
	}
}

// addWatchDirs recursively adds all directories under root to the watcher.
func addWatchDirs(watcher *fsnotify.Watcher, root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := watcher.Add(path); err != nil {
				return fmt.Errorf("failed to watch %s: %w", path, err)
			}
		}
		return nil
	})
}
