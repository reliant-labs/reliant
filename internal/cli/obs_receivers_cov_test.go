package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/exitcode"
)

func TestObsReceiversList(t *testing.T) {
	reply := `{"receivers":[
	  {"id":"r1","name":"oncall","type":"RECEIVER_TYPE_SLACK","settings":{"channel":"#oncall","a":"1"},"hasSecret":true},
	  {"id":"r2","name":"team","type":"RECEIVER_TYPE_EMAIL","settings":{"to":"ops@example.com"}}]}`

	t.Run("table lowercases the type and sorts the settings", func(t *testing.T) {
		c := covAEnvCaller(map[string]string{procListReceivers: reply})
		out, _, err := covARunObsAdmin(t, c, nil, "receivers", "list")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"ID", "SETTINGS", "oncall", "slack", "a=1 channel=#oncall", "email", "to=ops@example.com"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "RECEIVER_TYPE") {
			t.Errorf("the wire enum prefix leaked:\n%s", out)
		}
	})

	t.Run("none says where alerts go instead", func(t *testing.T) {
		c := covAEnvCaller(map[string]string{procListReceivers: `{}`})
		out, _, err := covARunObsAdmin(t, c, nil, "receivers", "list")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "alerts reach the Reliant console's inbox only") {
			t.Errorf("out = %q", out)
		}
	})

	t.Run("json", func(t *testing.T) {
		c := covAEnvCaller(map[string]string{procListReceivers: reply})
		out, _, err := covARunObsAdmin(t, c, nil, "receivers", "list", "--json")
		if err != nil {
			t.Fatal(err)
		}
		doc := decode(t, out)
		rs, _ := doc["receivers"].([]any)
		if doc["schema"] != "forge.obs.receivers/v1" || len(rs) != 2 {
			t.Errorf("doc = %v", doc)
		}
		if first := rs[0].(map[string]any); first["hasSecret"] != true || first["id"] != "r1" {
			t.Errorf("first = %v", first)
		}
	})

	t.Run("an old control plane is could-not-look", func(t *testing.T) {
		c := covAEnvCaller(nil)
		c.errs = map[string]error{procListReceivers: &cloud.Error{Code: cloud.CodeUnimplemented}}
		_, _, err := covARunObsAdmin(t, c, nil, "receivers", "list")
		if err == nil || !strings.Contains(err.Error(), "does not serve alerting") || exitcode.Of(err) != 2 {
			t.Fatalf("err = %v exit %d", err, exitcode.Of(err))
		}
	})
}

func TestObsReceiversCreate(t *testing.T) {
	created := `{"receiver":{"id":"r9","name":"oncall","type":"RECEIVER_TYPE_SLACK"}}`

	t.Run("request carries the wire type, settings and secret", func(t *testing.T) {
		c := covAEnvCaller(map[string]string{procCreateReceiver: created})
		out, _, err := covARunObsAdmin(t, c, nil, "receivers", "create", "oncall", "--type", "Slack",
			"--secret", "https://hooks.example/x", "--setting", "channel=#oncall", "--setting", "url=https://a/b?c=d")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "created receiver oncall (r9)") || !strings.Contains(out, "forge obs receivers test r9") {
			t.Errorf("out = %q", out)
		}
		var req map[string]any
		if err := json.Unmarshal([]byte(covACall(t, c, procCreateReceiver).body), &req); err != nil {
			t.Fatal(err)
		}
		set, _ := req["settings"].(map[string]any)
		if req["type"] != "RECEIVER_TYPE_SLACK" || req["name"] != "oncall" || req["secret"] != "https://hooks.example/x" || req["orgId"] != "org-1" ||
			set["channel"] != "#oncall" || set["url"] != "https://a/b?c=d" {
			t.Errorf("request = %v", req)
		}
	})

	t.Run("json prints the receiver", func(t *testing.T) {
		c := covAEnvCaller(map[string]string{procCreateReceiver: created})
		out, _, err := covARunObsAdmin(t, c, nil, "receivers", "create", "oncall", "--type", "pagerduty", "--json")
		if err != nil {
			t.Fatal(err)
		}
		if doc := decode(t, out); doc["id"] != "r9" || doc["name"] != "oncall" {
			t.Errorf("doc = %v", doc)
		}
	})

	refusals := []struct {
		name string
		args []string
		want string
	}{
		{"unknown type", []string{"--type", "carrier-pigeon"}, `--type "carrier-pigeon" is not a receiver type`},
		{"setting without =", []string{"--type", "slack", "--setting", "channel"}, `--setting "channel" is not key=value`},
		{"setting with an empty key", []string{"--type", "slack", "--setting", "=v"}, `--setting "=v" is not key=value`},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			c := covAEnvCaller(nil)
			_, cp, err := covARunObsAdmin(t, c, nil, append([]string{"receivers", "create", "x"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) || exitcode.Of(err) != 1 {
				t.Fatalf("err = %v exit %d", err, exitcode.Of(err))
			}
			if *cp != "" || len(c.calls) != 0 {
				t.Errorf("a bad request must be refused before signing in (connected %q, calls %v)", *cp, c.calls)
			}
		})
	}

	t.Run("--type is required", func(t *testing.T) {
		_, _, err := covARunObsAdmin(t, covAEnvCaller(nil), nil, "receivers", "create", "x")
		if err == nil || !strings.Contains(err.Error(), `"type"`) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("without obs:write the person is told what is missing", func(t *testing.T) {
		c := covAEnvCaller(nil)
		c.errs = map[string]error{procCreateReceiver: &cloud.Error{Code: cloud.CodePermissionDenied, Message: "x"}}
		_, _, err := covARunObsAdmin(t, c, nil, "receivers", "create", "x", "--type", "email")
		if err == nil || !strings.Contains(err.Error(), "needs a person holding the obs:write permission") || exitcode.Of(err) != 1 {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestObsReceiversTest(t *testing.T) {
	tests := []struct {
		name, reply, want string
		wantErr           string
	}{
		{"accepted and live", `{"accepted":true,"receiverLive":true}`, "test alert accepted: it should arrive within a minute", ""},
		{"accepted but not yet live", `{"accepted":true}`, "the receiver is not live in the alert router yet", ""},
		{"not accepted", `{}`, "", "the alert router did not accept the test alert"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := covAEnvCaller(map[string]string{procTestReceiver: tc.reply})
			out, _, err := covARunObsAdmin(t, c, nil, "receivers", "test", "r1")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || exitcode.Of(err) != 2 {
					t.Fatalf("err = %v exit %d, want could-not-look", err, exitcode.Of(err))
				}
				return
			}
			if err != nil || !strings.Contains(out, tc.want) {
				t.Fatalf("out = %q err = %v", out, err)
			}
			if body := covACall(t, c, procTestReceiver).body; !strings.Contains(body, `"id":"r1"`) {
				t.Errorf("request = %s", body)
			}
		})
	}
	t.Run("a refusal is mapped", func(t *testing.T) {
		c := covAEnvCaller(nil)
		c.errs = map[string]error{procTestReceiver: &cloud.Error{Code: cloud.CodeNotFound}}
		if _, _, err := covARunObsAdmin(t, c, nil, "receivers", "test", "nope"); exitcode.Of(err) != 1 {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestObsReceiversDelete(t *testing.T) {
	c := covAEnvCaller(map[string]string{procDeleteReceiver: `{}`})
	out, _, err := covARunObsAdmin(t, covANilOutCaller{c}, nil, "receivers", "delete", "r1")
	if err != nil || out != "deleted receiver r1\n" {
		t.Fatalf("out = %q err = %v", out, err)
	}
	if body := covACall(t, c, procDeleteReceiver).body; !strings.Contains(body, `"id":"r1"`) || !strings.Contains(body, `"orgId":"org-1"`) {
		t.Errorf("request = %s", body)
	}
	c = covAEnvCaller(nil)
	c.errs = map[string]error{procDeleteReceiver: &cloud.Error{Code: cloud.CodePermissionDenied}}
	if _, _, err := covARunObsAdmin(t, c, nil, "receivers", "delete", "r1"); err == nil || !strings.Contains(err.Error(), "obs:write") {
		t.Fatalf("err = %v", err)
	}
}

func TestSettingsText(t *testing.T) {
	if got := settingsText(nil); got != "" {
		t.Errorf("got %q", got)
	}
	if got := settingsText(map[string]string{"z": "1", "a": "2", "m": ""}); got != "a=2 m= z=1" {
		t.Errorf("got %q", got)
	}
}

// covANilOutCaller lets a verb pass a nil reply target, as the real client
// accepts, which fakeCloudCaller's json.Unmarshal does not.
type covANilOutCaller struct{ *fakeCloudCaller }

func (c covANilOutCaller) Call(ctx context.Context, procedure string, req, out any) error {
	if out == nil {
		var discard json.RawMessage
		out = &discard
	}
	return c.fakeCloudCaller.Call(ctx, procedure, req, out)
}
