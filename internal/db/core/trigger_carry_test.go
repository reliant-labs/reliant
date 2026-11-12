// Copyright (c) 2025 Reliant Labs
package core

import (
	"encoding/json"
	"testing"
)

func TestCarryActivationConfig(t *testing.T) {
	cases := map[string]struct{ prev, next, want string }{
		"carries the installation":    {`{"integration":"github","installation":"42"}`, `{"integration":"github","events":["a"]}`, `{"integration":"github","events":["a"],"installation":"42"}`},
		"the activation's wins":       {`{"installation":"42"}`, `{"integration":"github","installation":"7"}`, `{"integration":"github","installation":"42"}`},
		"nothing bound":               {`{"integration":"github"}`, `{"integration":"github","events":["a"]}`, `{"integration":"github","events":["a"]}`},
		"unreadable prev":             {`not json`, `{"integration":"github"}`, `{"integration":"github"}`},
		"unreadable next is returned": {`{"installation":"42"}`, `not json`, `not json`},
		"a schedule's array next":     {`{"installation":"42"}`, `[]`, `[]`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := CarryActivationConfig(json.RawMessage(tc.prev), json.RawMessage(tc.next))
			if tc.want == tc.next && string(got) == tc.next {
				return
			}
			var g, w any
			if json.Unmarshal(got, &g) != nil || json.Unmarshal([]byte(tc.want), &w) != nil {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
			gb, _ := json.Marshal(g)
			wb, _ := json.Marshal(w)
			if string(gb) != string(wb) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
