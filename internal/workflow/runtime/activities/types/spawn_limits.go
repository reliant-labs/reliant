// Copyright (c) 2025 Reliant Labs
package types

// How many sub-agents may run at once. Spawning nests tools.MaxSpawnDepth
// levels deep (reliant#2757), so without a cap one orchestrator's fan-out
// times each child's fan-out could start dozens of agents. Both caps are
// enforced by the workflow runtime when it dispatches a batch's spawn calls: a
// spawn past either is refused with an explanation and nothing is started.

const (
	// DefaultMaxConcurrentSubAgents is how many sub-agents one agent may have
	// running at once. Above about six the provider throttles (the spawn tool
	// says so), so 8 leaves room for a short burst without letting a runaway
	// loop fan out without bound.
	DefaultMaxConcurrentSubAgents = 8
	// DefaultMaxConcurrentSubAgentsTotal is how many sub-agents may run at
	// once across a whole run, every depth counted.
	DefaultMaxConcurrentSubAgentsTotal = 16

	// MaxConcurrentSubAgentsInput is the run input that overrides
	// DefaultMaxConcurrentSubAgents.
	MaxConcurrentSubAgentsInput = "max_concurrent_sub_agents"
	// MaxConcurrentSubAgentsTotalInput is the run input that overrides
	// DefaultMaxConcurrentSubAgentsTotal.
	MaxConcurrentSubAgentsTotalInput = "max_concurrent_sub_agents_total"
)

// SpawnLimits are a run's sub-agent concurrency caps. The zero value means
// the defaults.
type SpawnLimits struct {
	// PerAgent caps one agent's running sub-agents.
	PerAgent int
	// Total caps the run's running sub-agents, at every depth.
	Total int
}

// SpawnLimitsFromInputs reads the caps from a run's inputs. A missing,
// non-numeric or non-positive value keeps that cap's default.
func SpawnLimitsFromInputs(inputs map[string]interface{}) SpawnLimits {
	return SpawnLimits{
		PerAgent: positiveInt(inputs[MaxConcurrentSubAgentsInput], DefaultMaxConcurrentSubAgents),
		Total:    positiveInt(inputs[MaxConcurrentSubAgentsTotalInput], DefaultMaxConcurrentSubAgentsTotal),
	}
}

// Resolved returns l with every unset cap at its default.
func (l SpawnLimits) Resolved() SpawnLimits {
	if l.PerAgent <= 0 {
		l.PerAgent = DefaultMaxConcurrentSubAgents
	}
	if l.Total <= 0 {
		l.Total = DefaultMaxConcurrentSubAgentsTotal
	}
	return l
}

func positiveInt(v interface{}, fallback int) int {
	var n int
	switch x := v.(type) {
	case int:
		n = x
	case int32:
		n = int(x)
	case int64:
		n = int(x)
	case float64:
		n = int(x)
	}
	if n <= 0 {
		return fallback
	}
	return n
}
