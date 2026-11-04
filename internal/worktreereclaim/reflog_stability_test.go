// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReflogActivity_DefersChangesDuringRead(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"append", "truncate", "mtime", "replace", "remove"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "HEAD")
			data := activityRecord(time.Unix(1700000000, 0), "+0000")
			write(t, path, data)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			mutated := false
			r := &activityHookReader{src: strings.NewReader(data), hook: func() {
				if mutated {
					return
				}
				mutated = true
				switch change {
				case "append":
					write(t, path, data+data)
				case "truncate":
					write(t, path, "")
				case "mtime":
					setIdleMtime(t, path, before.ModTime().Add(time.Hour))
				case "replace":
					other := path + ".new"
					write(t, other, data)
					setIdleMtime(t, other, before.ModTime())
					if err := os.Rename(other, path); err != nil {
						t.Fatal(err)
					}
				case "remove":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
			}}
			if _, err := stableReflogActivity(context.Background(), path, f, before, r); err == nil {
				t.Fatal("changed reflog returned ancient activity instead of deferring")
			}
		})
	}
}
