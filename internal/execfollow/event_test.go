// Copyright (c) 2025 Reliant Labs
package execfollow

import "testing"

func TestParseHookFlag(t *testing.T) {
	cases := []struct {
		raw     string
		want    Hook
		wantErr bool
	}{
		{raw: "on=node_failed cmd=echo hi", want: Hook{On: "node_failed", Cmd: "echo hi"}},
		{raw: "on=any cmd=notify.sh --level=warn 'a b'", want: Hook{On: "any", Cmd: "notify.sh --level=warn 'a b'"}},
		{raw: "on=workflow_completed   cmd=cat > /tmp/x", want: Hook{On: "workflow_completed", Cmd: "cat > /tmp/x"}},
		{raw: "  on=node_started cmd=true  ", want: Hook{On: "node_started", Cmd: "true"}},
		// Command containing "cmd=" again keeps everything after the first.
		{raw: "on=node_completed cmd=env cmd=x", want: Hook{On: "node_completed", Cmd: "env cmd=x"}},
		{raw: "node_failed cmd=echo", wantErr: true},         // missing on=
		{raw: "on=node_failed", wantErr: true},               // missing cmd=
		{raw: "on=node_failed echo hi", wantErr: true},       // missing cmd= prefix
		{raw: "on=bogus_event cmd=echo hi", wantErr: true},   // unknown event
		{raw: "on=workflow_started cmd=echo", wantErr: true}, // not hookable (only via any)
		{raw: "on=node_failed cmd=", wantErr: true},          // empty command
		{raw: "", wantErr: true},
	}

	for _, tc := range cases {
		got, err := ParseHookFlag(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseHookFlag(%q) expected error, got %+v", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseHookFlag(%q): %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseHookFlag(%q) = %+v, want %+v", tc.raw, got, tc.want)
		}
	}
}

func TestHookMatches(t *testing.T) {
	cases := []struct {
		hook  Hook
		event string
		want  bool
	}{
		{Hook{On: "node_failed"}, EventNodeFailed, true},
		{Hook{On: "node_failed"}, EventNodeCompleted, false},
		{Hook{On: "workflow_completed"}, EventWorkflowCompleted, true},
		{Hook{On: "workflow_failed"}, EventWorkflowFailed, true},
		{Hook{On: "workflow_failed"}, EventWorkflowCancelled, false},
		{Hook{On: "any"}, EventNodeStarted, true},
		{Hook{On: "any"}, EventWorkflowCancelled, true},
		{Hook{On: "any"}, EventFollowTimeout, true},
	}
	for _, tc := range cases {
		if got := tc.hook.Matches(tc.event); got != tc.want {
			t.Errorf("Hook{On:%q}.Matches(%q) = %v, want %v", tc.hook.On, tc.event, got, tc.want)
		}
	}
}
