package wfyaml

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const checkGraph = "\nentry: [a]\nnodes: [{id: a, type: compact}]\n"

func TestWorkflowCheckRoundTrip(t *testing.T) {
	for _, check := range []string{
		"reliant/ci",
		"{name: reliant/ci}",
		"{name: reliant/ci, timeout: 2h}",
		"{name: lint, repository: '{{ trigger.payload.attributes.repository }}', head_sha: '{{ trigger.payload.data.check_suite.head_sha }}', output: '{{ nodes.a.check }}', timeout: 30m}",
	} {
		t.Run(check, func(t *testing.T) {
			wf, err := ParseWorkflow([]byte("name: checked\ncheck: " + check + checkGraph))
			require.NoError(t, err)
			require.NotNil(t, wf.GetCheck())
			encoded, err := MarshalWorkflow(wf)
			require.NoError(t, err)
			again, err := ParseWorkflow(encoded)
			require.NoError(t, err)
			require.Equal(t, wf, again)
		})
	}
}

func TestWorkflowCheckFields(t *testing.T) {
	wf, err := ParseWorkflow([]byte("name: checked\ncheck:\n  name: ' reliant/ci '\n  head_sha: '{{ trigger.payload.data.after }}'\n  output: '{{ nodes.a.check }}'\n  timeout: 45m" + checkGraph))
	require.NoError(t, err)
	check := wf.GetCheck()
	require.Equal(t, "reliant/ci", check.GetName())
	require.Equal(t, "{{ trigger.payload.data.after }}", check.GetHeadSha())
	require.Equal(t, "{{ nodes.a.check }}", check.GetOutput())
	require.Equal(t, "45m", check.GetTimeout())
	require.Empty(t, check.GetRepository())

	short, err := ParseWorkflow([]byte("name: checked\ncheck: reliant/review" + checkGraph))
	require.NoError(t, err)
	require.Equal(t, "reliant/review", short.GetCheck().GetName())
	encoded, err := MarshalWorkflow(short)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "check: reliant/review")
}

func TestWorkflowCheckRejectsMalformed(t *testing.T) {
	for _, check := range []string{
		"''",
		"{}",
		"{repository: a/b}",
		"[reliant/ci]",
		"{name: ci, name: ci2}",
		"{name: ci, conclusion: success}",
		"{name: ci, output: {title: x}}",
	} {
		t.Run(check, func(t *testing.T) {
			_, err := ParseWorkflow([]byte("name: checked\ncheck: " + check + checkGraph))
			require.Error(t, err)
		})
	}
}

func TestWorkflowCheckAbsentRemainsAbsent(t *testing.T) {
	wf, err := ParseWorkflow([]byte("name: plain" + checkGraph))
	require.NoError(t, err)
	require.Nil(t, wf.GetCheck())
	encoded, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "check")
}

func TestWorkflowCheckIsRuntimeEvaluated(t *testing.T) {
	require.True(t, IsRuntimeEvaluatedTopLevelKey("check"))
}
