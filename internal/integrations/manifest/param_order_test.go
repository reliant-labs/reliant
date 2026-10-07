package manifest

import (
	"slices"
	"strings"
	"testing"
)

// A Struct keeps no key order, so the loader records the order params are
// declared in: it is the order a form lists them, and Slack's Post message
// declares Channel and Text before Blocks for a reason. An alias (a param
// shared across actions with a YAML anchor) keeps its place too.
func TestParseRecordsDeclaredParamOrder(t *testing.T) {
	doc := `
id: demo
version: 1
display_name: Demo
connection:
  base_url: https://api.example.com/v1
actions:
  - id: message.post
    placement: server
    params:
      type: object
      properties:
        channel: &channel { type: string }
        text: { type: string }
        blocks: { type: array }
        thread_ts: { type: string }
    request: { method: POST, path: /post }
  - id: message.update
    placement: server
    params:
      type: object
      properties:
        ts: { type: string }
        channel: *channel
        text: { type: string }
    request: { method: POST, path: /update }
`
	m, err := Parse([]byte(doc), TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range [][]string{{"channel", "text", "blocks", "thread_ts"}, {"ts", "channel", "text"}} {
		if got := m.GetActions()[i].GetParamOrder(); !slices.Equal(got, want) {
			t.Errorf("%s param_order = %v, want %v", m.GetActions()[i].GetId(), got, want)
		}
	}
}

// param_order is derived; a manifest that writes it would be a second,
// drifting source of the order.
func TestParseRejectsAuthoredParamOrder(t *testing.T) {
	doc := strings.Replace(validHTTP, "    params:\n", "    param_order: [id]\n    params:\n", 1)
	if _, err := Parse([]byte(doc), TrustCurated); err == nil || !strings.Contains(err.Error(), "param_order") {
		t.Fatalf("an authored param_order must be a load error, got %v", err)
	}
}
