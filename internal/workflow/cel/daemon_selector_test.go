package wfcel

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// A node's `daemon: "{{ ... }}"` resolves to a DaemonSelectorProto from
// whatever the expression returns, pool included.
func TestResolveCELFields_CelDaemonSelectorExpr(t *testing.T) {
	tests := []struct {
		name   string
		result interface{}
		want   *reliantv1.DaemonSelectorProto
	}{
		{"pool", map[string]interface{}{"pool": "gate"}, &reliantv1.DaemonSelectorProto{Pool: "gate"}},
		{"labels", map[string]interface{}{"labels": map[string]interface{}{"role": "gate"}},
			&reliantv1.DaemonSelectorProto{Labels: map[string]string{"role": "gate"}}},
		{"name", map[string]interface{}{"name": "box-1"}, &reliantv1.DaemonSelectorProto{Name: "box-1"}},
		{"every field", map[string]interface{}{"id": "d-1", "name": "n", "type": "cloud", "pool": "p", "labels": map[string]interface{}{"a": "b"}},
			&reliantv1.DaemonSelectorProto{Id: "d-1", Name: "n", Type: "cloud", Pool: "p", Labels: map[string]string{"a": "b"}}},
		{"non-string pool is ignored", map[string]interface{}{"pool": int64(3)}, &reliantv1.DaemonSelectorProto{}},
		{"string shorthand type", "cloud", &reliantv1.DaemonSelectorProto{Type: "cloud"}},
		{"string shorthand name", "box-1", &reliantv1.DaemonSelectorProto{Name: "box-1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			node := &reliantv1.Node{
				Id:     "step",
				Type:   "run",
				Daemon: &reliantv1.CelDaemonSelector{Value: &reliantv1.CelDaemonSelector_Expr{Expr: "{{inputs.machine}}"}},
			}
			resolved, err := ResolveCELFields(node, newMockEvaluator(map[string]interface{}{"{{inputs.machine}}": tc.result}))
			require.NoError(t, err)
			got := resolved.(*reliantv1.Node).GetDaemon().GetLiteral()
			require.NotNil(t, got)
			assert.True(t, proto.Equal(tc.want, got), "want %v, got %v", tc.want, got)
		})
	}
}
