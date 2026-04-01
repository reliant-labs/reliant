package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	skillcatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	"github.com/stretchr/testify/require"
)

func TestParityFixtures_ParseSkillMarkdown(t *testing.T) {
	fixturesRoot := filepath.Join("testdata", "parity")
	entries, err := os.ReadDir(fixturesRoot)
	require.NoError(t, err)

	type fixture struct {
		name      string
		dir       string
		skillPath string
	}
	fixtures := make([]fixture, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		suiteDir := filepath.Join(fixturesRoot, entry.Name())
		skillPath := ""
		walkErr := filepath.WalkDir(suiteDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if strings.EqualFold(d.Name(), "SKILL.md") {
				skillPath = path
				return filepath.SkipAll
			}
			return nil
		})
		require.NoError(t, walkErr)
		require.NotEmpty(t, skillPath, "fixture %s must contain a SKILL.md", entry.Name())
		fixtures = append(fixtures, fixture{name: entry.Name(), dir: suiteDir, skillPath: skillPath})
	}

	sort.Slice(fixtures, func(i, j int) bool { return fixtures[i].name < fixtures[j].name })

	for _, tc := range fixtures {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			blob, err := os.ReadFile(tc.skillPath)
			require.NoError(t, err)

			skill, parseErr := skillcatalog.ParseSkillMarkdown(tc.skillPath, ScopeProject, blob)
			if strings.HasPrefix(tc.name, "valid-") {
				require.NoError(t, parseErr)
				require.NotEmpty(t, skill.Name)
				require.NotEmpty(t, skill.NormalizedKey)
				if tc.name == "valid-unicode-nfkc" {
					require.Equal(t, "résumé-helper", skill.Name)
					require.Equal(t, "résumé-helper", skill.NormalizedKey)
				}
				return
			}

			require.Error(t, parseErr)
			switch tc.name {
			case "invalid-unknown-field":
				require.Contains(t, parseErr.Error(), "unexpected fields in frontmatter")
			case "invalid-name-parent-mismatch":
				require.Contains(t, parseErr.Error(), "must match parent directory")
			case "invalid-uppercase":
				require.Contains(t, parseErr.Error(), "must be lowercase")
			case "invalid-consecutive-hyphen":
				require.Contains(t, parseErr.Error(), "must not contain consecutive hyphens")
			case "invalid-empty-description":
				require.Contains(t, parseErr.Error(), "missing required field: description")
			default:
				require.Failf(t, "unknown invalid fixture", "add assertion mapping for fixture %s", tc.name)
			}
		})
	}
}
