package store

// ProjectMergeColumnPolicies is every "table.column" holding a project id that
// the merge declares (projectMergeColumns), with its policy, for the external
// tests.
func ProjectMergeColumnPolicies() map[string]string {
	out := map[string]string{}
	for _, c := range projectMergeColumns {
		out[c.table+"."+c.column] = string(c.policy)
	}
	return out
}

// ProjectMergeLeftInPlace is the policy of a column the merge leaves naming the
// source on purpose.
const ProjectMergeLeftInPlace = string(projectMergeLeave)

// ProjectMergeRepoRefColumns and ProjectMergeWorktreeRefColumns are the
// "table.column"s holding a repo or worktree id the merge declares.
func ProjectMergeRepoRefColumns() []string { return projectMergeRefColumns(projectMergeRepoRefs) }

func ProjectMergeWorktreeRefColumns() []string {
	return projectMergeRefColumns(projectMergeWorktreeRefs)
}

func projectMergeRefColumns(refs []projectMergeRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.table+"."+r.column)
	}
	return out
}
