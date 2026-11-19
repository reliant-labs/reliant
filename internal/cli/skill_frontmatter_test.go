package cli

import (
	"strings"
	"testing"
)

// TestParseFrontmatter_BlockScalarDescription covers the YAML folded/literal
// block scalar. The hand-rolled line splitter read `description: >-` as the
// LITERAL value ">-" and dropped the indented continuation lines that carried
// the actual sentence, so the skill listed with a two-character description
// and searched as if it had none.
func TestParseFrontmatter_BlockScalarDescription(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{
			name: "folded",
			src:  "---\nname: harness\ndescription: >-\n  Create validation harnesses to verify fixes\n  and features work end-to-end.\n---\nbody\n",
			want: "Create validation harnesses to verify fixes and features work end-to-end.",
		},
		{
			name: "literal",
			src:  "---\nname: harness\ndescription: |\n  First line.\n  Second line.\n---\nbody\n",
			want: "First line.\nSecond line.",
		},
		{
			name: "plain",
			src:  "---\nname: db\ndescription: Database work — migrations are the source of truth.\n---\nbody\n",
			want: "Database work — migrations are the source of truth.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseFrontmatter([]byte(tc.src))
			if strings.TrimSpace(got.Description) != tc.want {
				t.Errorf("description = %q, want %q", got.Description, tc.want)
			}
		})
	}
}

// TestShippedSkillsHaveRealDescriptions is the corpus-wide guard: every skill
// forge ships must list with a sentence, not a YAML artifact. The grouped
// listing is an index — a row whose description is ">-" tells the reader
// nothing and is indistinguishable from a skill with no description at all.
func TestShippedSkillsHaveRealDescriptions(t *testing.T) {
	skills, err := listForgeShippedSkills()
	if err != nil {
		t.Fatalf("listForgeShippedSkills: %v", err)
	}
	for _, s := range skills {
		desc := strings.TrimSpace(s.Description)
		if len(desc) < 20 {
			t.Errorf("skill %q has description %q — too short to be a real one; check its YAML frontmatter", s.Path, s.Description)
		}
	}
}
