// Copyright (c) 2025 Reliant Labs
package replaytest

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// frozenSumsFile lists every fixture in a frozen set with its sha256, in
// `sha256sum` format, so `shasum -a 256 -c SHA256SUMS` checks a set by hand.
const frozenSumsFile = "SHA256SUMS"

// TestFrozenFixturesAreUnchanged keeps the frozen sets frozen. A frozen set is
// only worth anything if it still holds the histories that were recorded
// before a change: regenerate one and it silently becomes another copy of the
// current shape, which is exactly the hole #641 fell through. So the bytes are
// pinned, and adding, editing or deleting a frozen fixture is a test failure
// rather than a diff someone has to notice in review.
//
// A new set is made with `make freeze-replay-fixtures NAME=...`, which writes
// its SHA256SUMS; nothing should ever need to touch one after that. A set is
// retired only by deleting the whole directory together with the version gate
// it exists for, once no run that old can still be open.
func TestFrozenFixturesAreUnchanged(t *testing.T) {
	sets, err := filepath.Glob(filepath.Join("fixtures", "frozen", "*"))
	require.NoError(t, err)
	require.NotEmpty(t, sets, "no frozen fixture sets: the replay test is only checking histories the current code recorded itself")

	for _, set := range sets {
		info, err := os.Stat(set)
		require.NoError(t, err)
		if !info.IsDir() {
			continue
		}
		t.Run(filepath.Base(set), func(t *testing.T) {
			want := readFrozenSums(t, filepath.Join(set, frozenSumsFile))

			onDisk, err := filepath.Glob(filepath.Join(set, "*.json"))
			require.NoError(t, err)
			got := make(map[string]string, len(onDisk))
			for _, path := range onDisk {
				raw, err := os.ReadFile(path)
				require.NoError(t, err)
				sum := sha256.Sum256(raw)
				got[filepath.Base(path)] = hex.EncodeToString(sum[:])
			}

			assert.Equal(t, sortedKeys(want), sortedKeys(got),
				"a frozen set's fixtures were added or removed; frozen sets are never changed after they are made")
			for name, sum := range want {
				if got[name] != "" {
					assert.Equal(t, sum, got[name],
						"frozen fixture %s was modified; it must keep the history it was recorded with", name)
				}
			}
		})
	}
}

func readFrozenSums(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err, "every frozen set needs a %s (make freeze-replay-fixtures writes it)", frozenSumsFile)
	defer func() { _ = f.Close() }()

	sums := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		require.Len(t, fields, 2, "malformed %s line: %q", frozenSumsFile, line)
		sums[strings.TrimPrefix(fields[1], "*")] = fields[0]
	}
	require.NoError(t, scanner.Err())
	require.NotEmpty(t, sums, "%s lists no fixtures", path)
	return sums
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
