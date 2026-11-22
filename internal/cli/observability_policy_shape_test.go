package cli

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestShapeObservabilityPolicyOf(t *testing.T) {
	entities := func(p *ObservabilityPolicyEntity) *KCLEntities {
		return &KCLEntities{Observability: &ObservabilityEntity{Enabled: true, Policy: p}}
	}
	cases := []struct {
		name string
		in   *KCLEntities
		want *release.ShapeObservabilityPolicy
	}{
		{"no entities", nil, nil},
		{"no observability", &KCLEntities{}, nil},
		{"opts out of nothing", entities(&ObservabilityPolicyEntity{Enabled: true}), nil},
		{"per category", entities(&ObservabilityPolicyEntity{Enabled: true, Disabled: []string{"logs", "metadata"}}),
			&release.ShapeObservabilityPolicy{Enabled: true, Disabled: []string{"logs", "metadata"}}},
		{"replay only", entities(&ObservabilityPolicyEntity{Enabled: true, OptIn: []string{"replay"}}),
			&release.ShapeObservabilityPolicy{Enabled: true, OptIn: []string{"replay"}}},
		{"everything", entities(&ObservabilityPolicyEntity{Enabled: false, Disabled: release.ObsCategories}),
			&release.ShapeObservabilityPolicy{Enabled: false, Disabled: release.ObsCategories}},
	}
	for _, c := range cases {
		if got := shapeObservabilityPolicyOf(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}
