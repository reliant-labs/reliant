package config

import (
	"sort"
	"time"
)

// A machine's snapshot also carries the project's skills, repo memories and
// scenarios: per-file content that, like workflows, a machine without a
// nested checkout simply does not have. Each machine's is kept on its own
// (project_machine_catalogs), a run reads its machine's, and the project's
// record holds their union, so one machine syncing without a checkout cannot
// erase that checkout's skills for everyone. The union keeps one entry per
// key, from the machine that pushed it most recently (ties to the lower
// daemon id), as UnionWorkflows does.

// MachineFiles is one machine's per-file config of a project.
type MachineFiles struct {
	DaemonID     string
	PushedAt     time.Time
	Skills       []StoredSkill
	RepoMemories map[string]string
	Scenarios    []StoredScenario
}

// SkillFileKey is a synced skill's identity across machines: its scope, the
// nested repo it was found in, and its path (one skill path can be declared
// by several repos).
func SkillFileKey(s StoredSkill) string { return s.Scope + "|" + s.Source + "|" + s.SkillPath }

// ScenarioFileKey is a synced scenario's identity: its workflow and name.
func ScenarioFileKey(s StoredScenario) string { return s.WorkflowSlug + "/" + s.Name }

// UnionSkills is the project's skills over every machine's, sorted by key.
func UnionSkills(machines []MachineFiles) []StoredSkill {
	return unionByKey(machines, func(m MachineFiles) []StoredSkill { return m.Skills }, SkillFileKey)
}

// UnionScenarios is UnionSkills for scenarios.
func UnionScenarios(machines []MachineFiles) []StoredScenario {
	return unionByKey(machines, func(m MachineFiles) []StoredScenario { return m.Scenarios }, ScenarioFileKey)
}

// UnionRepoMemories is the project's repo memories (repo path -> content)
// over every machine's; nil when none has any.
func UnionRepoMemories(machines []MachineFiles) map[string]string {
	type memory struct{ repo, content string }
	merged := unionByKey(machines, func(m MachineFiles) []memory {
		out := make([]memory, 0, len(m.RepoMemories))
		for repo, content := range m.RepoMemories {
			out = append(out, memory{repo, content})
		}
		return out
	}, func(m memory) string { return m.repo })
	if len(merged) == 0 {
		return nil
	}
	out := make(map[string]string, len(merged))
	for _, m := range merged {
		out[m.repo] = m.content
	}
	return out
}

func unionByKey[T any](machines []MachineFiles, files func(MachineFiles) []T, key func(T) string) []T {
	ordered := append([]MachineFiles(nil), machines...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].PushedAt.Equal(ordered[j].PushedAt) {
			return ordered[i].PushedAt.After(ordered[j].PushedAt)
		}
		return ordered[i].DaemonID < ordered[j].DaemonID
	})
	byKey := map[string]T{}
	for _, m := range ordered {
		for _, f := range files(m) {
			if k := key(f); k != "" {
				if _, taken := byKey[k]; !taken {
					byKey[k] = f
				}
			}
		}
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]T, 0, len(keys))
	for _, k := range keys {
		out = append(out, byKey[k])
	}
	return out
}
