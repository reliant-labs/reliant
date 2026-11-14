// Copyright (c) 2025 Reliant Labs

package amconfig

import (
	"strings"
	"testing"

	"github.com/prometheus/alertmanager/template"
)

// Slack reads <!channel>, <@U1> and <https://evil|trusted> in message text as a
// broadcast, a mention and a disguised link. The summary embeds an issue title,
// which whoever holds the public ingest key chose, so the rendered text must
// carry none of them: Slack shows &lt; and &gt; as the literal characters.
func TestSlackTextEscapesLabelsAndAnnotations(t *testing.T) {
	tmpl, err := template.FromGlobs(nil)
	if err != nil {
		t.Fatal(err)
	}
	evil := `<!channel> Reliant security: re-authenticate at <https://evil.example|https://app.reliant.dev/login> <@U024BE7LH> &amp; done`
	data := template.Data{
		CommonLabels: template.KV{"alertname": "New<!here>Issue"},
		Alerts: template.Alerts{{
			Labels:      template.KV{"alertname": "New<!here>Issue", "service": "<!everyone>"},
			Annotations: template.KV{"summary": "New issue in api (prod): " + evil + ". Details: https://app.reliant.dev/forge/p/shop"},
		}},
	}
	for name, tpl := range map[string]string{"text": slackText, "title": slackTitle} {
		got, err := tmpl.ExecuteTextString(tpl, data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, bad := range []string{"<", ">"} {
			if strings.Contains(got, bad) {
				t.Errorf("%s still contains %q:\n%s", name, bad, got)
			}
		}
	}
	got, _ := tmpl.ExecuteTextString(slackText, data)
	for _, want := range []string{"&lt;!channel&gt;", "&lt;@U024BE7LH&gt;", "&lt;https://evil.example|https://app.reliant.dev/login&gt;", "&amp;amp; done", "*New&lt;!here&gt;Issue*", "(&lt;!everyone&gt;)", "Details: https://app.reliant.dev/forge/p/shop"} {
		if !strings.Contains(got, want) {
			t.Errorf("text lacks %q:\n%s", want, got)
		}
	}

	r, err := Render(Input{
		Settings:  baseSettings(),
		Receivers: []Receiver{{OrgID: "o", Name: "oncall", Type: TypeSlack, SecretRef: "o.oncall"}},
		Routes:    []Route{{OrgID: "o", EnvID: "e", Matchers: map[string]string{"severity": "critical"}, Receivers: []string{"oncall"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	yml := string(r.YAML)
	if !strings.Contains(yml, "reReplaceAll") || !strings.Contains(yml, "link_names: false") {
		t.Errorf("the rendered slack config does not carry the escaping:\n%s", yml)
	}
}
