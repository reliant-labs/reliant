package v1alpha1

import (
	"strings"
	"testing"
)

func autoscaled(a Autoscale) WorkloadSpec {
	s := svc()
	s.Autoscale = &a
	return s
}

// TestAutoscaleValidate covers every autoscale rule. Each holds under both
// profiles: a range that contradicts itself, or a field that contradicts a
// moving count, has no correct rendering anywhere.
func TestAutoscaleValidate(t *testing.T) {
	withSpec := func(a Autoscale, mutate func(*WorkloadSpec)) WorkloadSpec {
		s := autoscaled(a)
		mutate(&s)
		return s
	}
	ok := Autoscale{MinReplicas: 2, MaxReplicas: 6}
	cases := map[string]struct {
		spec WorkloadSpec
		want string // "" = valid
	}{
		"valid range":                {autoscaled(ok), ""},
		"min equals max":             {autoscaled(Autoscale{MinReplicas: 3, MaxReplicas: 3}), ""},
		"max at the cap":             {autoscaled(Autoscale{MinReplicas: 1, MaxReplicas: MaxAutoscaleReplicas}), ""},
		"target at the bounds (low)": {autoscaled(Autoscale{MinReplicas: 1, MaxReplicas: 2, CPUTargetPercent: 10}), ""},
		"target at the bounds (hi)":  {autoscaled(Autoscale{MinReplicas: 1, MaxReplicas: 2, CPUTargetPercent: 95}), ""},
		"worker":                     {withSpec(ok, func(s *WorkloadSpec) { s.Kind, s.Ports = KindWorker, nil }), ""},
		"replicas 1 beside it":       {withSpec(ok, func(s *WorkloadSpec) { s.Replicas = 1 }), ""},
		"strategy RollingUpdate":     {withSpec(ok, func(s *WorkloadSpec) { s.Strategy = StrategyRollingUpdate }), ""},

		"min zero":      {autoscaled(Autoscale{MinReplicas: 0, MaxReplicas: 3}), "autoscale.minReplicas must be at least 1 (got 0)"},
		"min above max": {autoscaled(Autoscale{MinReplicas: 5, MaxReplicas: 3}), "autoscale.minReplicas (5) must not exceed autoscale.maxReplicas (3)"},
		"max above cap": {autoscaled(Autoscale{MinReplicas: 1, MaxReplicas: 11}), "autoscale.maxReplicas must be at most 10 (got 11)"},
		"target low":    {autoscaled(Autoscale{MinReplicas: 1, MaxReplicas: 2, CPUTargetPercent: 5}), "autoscale.cpuTargetPercent must be 10-95 (got 5)"},
		"target high":   {autoscaled(Autoscale{MinReplicas: 1, MaxReplicas: 2, CPUTargetPercent: 96}), "autoscale.cpuTargetPercent must be 10-95 (got 96)"},
		"replicas beside it": {withSpec(ok, func(s *WorkloadSpec) { s.Replicas = 3 }),
			"autoscale owns the replica count; set autoscale.minReplicas"},
		"storage beside it": {withSpec(ok, func(s *WorkloadSpec) { s.StorageGiB = 5 }), "autoscale is not allowed with storageGiB"},
		"recreate beside it": {withSpec(ok, func(s *WorkloadSpec) { s.Strategy = StrategyRecreate }),
			"autoscale is not allowed with strategy Recreate"},
		"operator": {withSpec(ok, func(s *WorkloadSpec) { s.Kind, s.Ports = KindOperator, nil }), `autoscale is not supported for kind "operator"`},
		"job": {withSpec(ok, func(s *WorkloadSpec) { s.Kind, s.Ports, s.Args = KindJob, nil, []string{"x"} }),
			`autoscale is not supported for kind "job"`},
		"cron": {withSpec(ok, func(s *WorkloadSpec) {
			s.Kind, s.Ports, s.Args, s.Schedule = KindCron, nil, []string{"x"}, "@daily"
		}), `autoscale is not supported for kind "cron"`},
		"tool": {withSpec(ok, func(s *WorkloadSpec) { s.Kind, s.Ports = KindTool, nil }), `autoscale is not supported for kind "tool"`},
	}
	for name, c := range cases {
		for _, p := range []Profile{ProfileFull, ProfileRestricted} {
			// The kinds Restricted refuses outright are covered under Full.
			if p == ProfileRestricted && (c.spec.Kind == KindOperator || c.spec.Kind == KindCron || c.spec.Kind == KindTool) {
				continue
			}
			t.Run(name+"/"+p.String(), func(t *testing.T) {
				err := c.spec.Validate(p)
				if c.want == "" {
					if err != nil {
						t.Fatalf("Validate = %v, want nil", err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("Validate = %v, want it to contain %q", err, c.want)
				}
			})
		}
	}
}

// TestAutoscaleDefaultsAndMax: WithDefaults fills the CPU target without
// touching the caller's value, and EffectiveMaxReplicas reads the range.
func TestAutoscaleDefaultsAndMax(t *testing.T) {
	in := autoscaled(Autoscale{MinReplicas: 2, MaxReplicas: 6})
	d := in.WithDefaults()
	if d.Autoscale.CPUTargetPercent != DefaultAutoscaleCPUTargetPercent {
		t.Errorf("defaulted cpuTargetPercent = %d, want %d", d.Autoscale.CPUTargetPercent, DefaultAutoscaleCPUTargetPercent)
	}
	if in.Autoscale.CPUTargetPercent != 0 {
		t.Error("WithDefaults wrote through the caller's *Autoscale")
	}
	if got := (Autoscale{CPUTargetPercent: 40}).WithDefaults().CPUTargetPercent; got != 40 {
		t.Errorf("a stated target was replaced: %d", got)
	}
	for _, c := range []struct {
		spec WorkloadSpec
		want int32
	}{
		{in, 6},
		{WorkloadSpec{}, 1},
		{WorkloadSpec{Replicas: 3}, 3},
	} {
		if got := c.spec.EffectiveMaxReplicas(); got != c.want {
			t.Errorf("EffectiveMaxReplicas(%+v) = %d, want %d", c.spec, got, c.want)
		}
	}
}
