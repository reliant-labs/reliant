package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRecoveryInterruptedRollbackInventory(t *testing.T) {
	heavyUnderShort(t, "rollback ownership proof uses local Git subprocesses")
	ctx := context.Background()
	source := recoveryTestRepo(t)
	recoveryTestWrite(t, source, "second", []byte("captured second\n"), 0644)
	c, err := CaptureRecovery(ctx, source, recoveryTestDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"own-partial", "no-index-yet", "refs-dir", "refs-content", "untracked", "modified", "staged", "empty-directory", "unregistered"} {
		t.Run(mutation, func(t *testing.T) {
			branch := "recovered-" + mutation
			dest := filepath.Join(t.TempDir(), "stage")
			if mutation == "unregistered" {
				if err := os.Mkdir(dest, 0700); err != nil {
					t.Fatal(err)
				}
				recoveryTestWrite(t, dest, "mine", []byte("only copy"), 0644)
			} else {
				if err := RestoreRecoveryLinkedCheckout(ctx, source, source, dest, branch, c); err != nil {
					t.Fatal(err)
				}
				switch mutation {
				case "no-index-yet":
					index := strings.TrimSpace(string(recoveryTestGit(t, dest, "rev-parse", "--path-format=absolute", "--git-path", "index")))
					if err := os.Remove(index); err != nil {
						t.Fatal(err)
					}
				case "refs-dir", "refs-content":
					// git 2.48+ creates an empty per-worktree refs/ at `worktree
					// add`; older git does not. Make it exist on every host.
					admin := strings.TrimSpace(string(recoveryTestGit(t, dest, "rev-parse", "--absolute-git-dir")))
					if err := os.MkdirAll(filepath.Join(admin, "refs"), 0o755); err != nil {
						t.Fatal(err)
					}
					if mutation == "refs-content" {
						recoveryTestWrite(t, filepath.Join(admin, "refs"), "bisect", []byte("not ours"), 0644)
					}
				case "own-partial":
					if err := os.Remove(filepath.Join(dest, "second")); err != nil {
						t.Fatal(err)
					}
				case "untracked":
					recoveryTestWrite(t, dest, "mine", []byte("only copy"), 0644)
				case "modified":
					recoveryTestWrite(t, dest, "file.txt", []byte("user changed"), 0644)
				case "staged":
					recoveryTestWrite(t, dest, "file.txt", []byte("user staged"), 0644)
					recoveryTestGit(t, dest, "add", "file.txt")
				case "empty-directory":
					if err := os.Mkdir(filepath.Join(dest, "mine"), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Reflection keeps this regression source buildable against the old
			// head-only API, so the pre-fix run proves destructive behavior rather
			// than merely failing to compile after the stronger capture contract.
			rollback := reflect.ValueOf(RollbackInterruptedRecovery)
			arg := reflect.ValueOf(c)
			if rollback.Type().In(4).Kind() == reflect.String {
				arg = reflect.ValueOf(c.HeadOID)
			}
			result := rollback.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(source), reflect.ValueOf(dest), reflect.ValueOf(branch), arg})
			err = nil
			if !result[0].IsNil() {
				err = result[0].Interface().(error)
			}
			if mutation == "own-partial" || mutation == "no-index-yet" || mutation == "refs-dir" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(dest); !os.IsNotExist(err) {
					t.Fatal("owned partial checkout not reconciled")
				}
				if got := strings.TrimSpace(string(recoveryTestGit(t, source, "branch", "--list", branch))); got != "" {
					t.Fatal("owned branch left behind")
				}
				return
			}
			if err == nil {
				t.Fatal("changed/unknown staging was removed")
			}
			if _, err := os.Stat(dest); err != nil {
				t.Fatal("held directory lost")
			}
			if mutation == "untracked" || mutation == "unregistered" {
				b, err := os.ReadFile(filepath.Join(dest, "mine"))
				if err != nil || string(b) != "only copy" {
					t.Fatal("user bytes lost")
				}
			}
		})
	}
}
