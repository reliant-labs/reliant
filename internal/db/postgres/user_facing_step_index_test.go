package postgres

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// literalNotInList matches `activity_name NOT IN ( 'A', 'B', ... )`.
var literalNotInList = regexp.MustCompile(`(?s)activity_name\s+NOT\s+IN\s*\(([^)]*)\)`)

func notInLists(t *testing.T, path string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var lists [][]string
	for _, m := range literalNotInList.FindAllStringSubmatch(string(raw), -1) {
		var names []string
		for _, part := range strings.Split(m[1], ",") {
			names = append(names, strings.Trim(strings.TrimSpace(part), "'"))
		}
		sort.Strings(names)
		lists = append(lists, names)
	}
	return lists
}

// The BASIC step query repeats the internal-activity list as a literal, as
// does the partial index it relies on, because a partial index is only usable
// when the planner can prove its predicate from the query text. The two
// literals and workflowmodel.InternalActivities are one set in three places;
// if they drift the failure is a silent seq scan (or a missing indicator), not
// an error, so this pins them.
func TestUserFacingStepIndexPredicateMatchesInternalActivities(t *testing.T) {
	want := append([]string(nil), model.InternalActivities...)
	sort.Strings(want)

	sources := map[string]struct {
		path     string
		minLists int
	}{
		"BASIC step query":    {"queries/step_executions.sql", 2},
		"index migration":     {"../migrations/postgres/20261004010819_bound_chat_open_reads.sql", 1},
		"schema.sql snapshot": {"schema.sql", 0},
	}
	for name, src := range sources {
		lists := notInLists(t, src.path)
		if name == "schema.sql snapshot" {
			// pg_dump renders the predicate as <> ALL (ARRAY[...]).
			raw, err := os.ReadFile(src.path)
			require.NoError(t, err)
			m := regexp.MustCompile(`idx_step_executions_user_facing ON [^;]*?ARRAY\[([^\]]*)\]`).FindStringSubmatch(string(raw))
			require.NotNil(t, m, "idx_step_executions_user_facing missing from schema.sql")
			var names []string
			for _, part := range strings.Split(m[1], ",") {
				part = strings.TrimSpace(part)
				part = strings.TrimSuffix(part, "::text")
				names = append(names, strings.Trim(part, "'"))
			}
			sort.Strings(names)
			lists = [][]string{names}
			src.minLists = 1
		}
		require.GreaterOrEqual(t, len(lists), src.minLists, "%s: expected literal NOT IN lists in %s", name, src.path)
		for _, got := range lists {
			require.Equal(t, want, got, "%s (%s) drifted from workflowmodel.InternalActivities", name, src.path)
		}
	}
}
