package cli

import (
	"strings"
	"testing"
)

const procGetUserActivity = "controlplane.v1.ObservabilityService/GetUserActivity"

func TestObsUsersCloudRequestShapeAndDocument(t *testing.T) {
	fc := &fakeCloudCaller{replies: map[string]string{procGetUserActivity: `{"envs":[{"envId":"env-prod",
"current":{"active":"412","new":"86","returning":"326"},"previous":{"active":"388","new":"71","returning":"317"},
"days":[{"day":"2026-10-21","active":"120","new":"9","returning":"111"},{"day":"2026-10-22","active":"130","new":"12","returning":"118"}],
"seenTotal":"1284","retentionDays":90,"firstEventAt":"2026-08-01T00:00:00Z","clients":["backend","web"]}]}`}}
	out, _, err := obsRunCloud(t, fc, "users", "prod", "--json", "--series", "2", "--seen-total", "--service", "api")
	if err != nil {
		t.Fatal(err)
	}
	body := fc.calls[0].body
	for _, want := range []string{`"orgId":"org-1"`, `"envIds":["env-prod"]`, `"services":["api"]`, `"seriesDays":2`, `"wantSeenTotal":true`,
		`"timeRange":{"start":"2026-10-15T12:00:00Z","end":"2026-10-22T12:00:00Z"}`} {
		if !strings.Contains(body, want) {
			t.Errorf("request lacks %s:\n%s", want, body)
		}
	}
	doc := decode(t, out)
	if doc["schema"] != schemaObsUsers {
		t.Errorf("schema = %v", doc["schema"])
	}
	envs := doc["envs"].([]any)
	if len(envs) != 1 {
		t.Fatalf("envs = %v", envs)
	}
	e := envs[0].(map[string]any)
	cur := e["current"].(map[string]any)
	if e["env_id"] != "env-prod" || cur["active"] != float64(412) || cur["new"] != float64(86) || e["seen_total"] != float64(1284) {
		t.Errorf("env = %v", e)
	}
	if days := e["days"].([]any); len(days) != 2 || days[1].(map[string]any)["active"] != float64(130) {
		t.Errorf("days = %v", e["days"])
	}
}

func TestObsUsersTextShowsDeltasAndTheSeries(t *testing.T) {
	fc := &fakeCloudCaller{replies: map[string]string{procGetUserActivity: `{"envs":[{"envId":"env-prod",
"current":{"active":"110","new":"0","returning":"110"},"previous":{"active":"100","new":"5","returning":"95"},
"days":[{"day":"2026-10-21","active":"1"},{"day":"2026-10-22","active":"8"}],"firstEventAt":"2026-08-01T00:00:00Z","clients":["backend"]}]}`}}
	out, _, err := obsRunCloud(t, fc, "users", "prod", "--series", "2")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"env-prod", "110", "+10%", "-100%", "backend", "▁█"} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
}

func TestObsUsersRefusesWhatItCannotRead(t *testing.T) {
	for _, args := range [][]string{
		{"users", "prod", "--series", "91"},
		{"users", "prod", "--since", "2400h"},
		{"users", "prod", "--filter", "k=v"},
	} {
		fc := &fakeCloudCaller{}
		if _, _, err := obsRunCloud(t, fc, args...); err == nil {
			t.Errorf("%v: accepted", args)
		}
		if len(fc.calls) != 0 {
			t.Errorf("%v: a refused request reached the control plane", args)
		}
	}
}

func TestUserDelta(t *testing.T) {
	for _, c := range []struct {
		cur, prev int64
		want      string
	}{{0, 0, "-"}, {3, 0, "new"}, {110, 100, "+10%"}, {50, 100, "-50%"}, {100, 100, "+0%"}} {
		if got := userDelta(c.cur, c.prev); got != c.want {
			t.Errorf("userDelta(%d, %d) = %q, want %q", c.cur, c.prev, got, c.want)
		}
	}
}
