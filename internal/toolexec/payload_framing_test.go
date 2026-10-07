// Copyright (c) 2025 Reliant Labs
package toolexec

import "testing"

func TestPayloadFraming(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		payload       string
		opens, closes bool
	}{
		{"complete envelope", `{"path":"/a","data":"c2VjcmV0"}`, true, true},
		{"truncated transfer", `{"path":"/a","data":"c2Vj`, true, false},
		{"not json", `garbage`, false, false},
		{"whitespace padded", "  {\"a\":1}\n", true, true},
		{"empty", "", false, false},
	}
	for _, tc := range cases {
		opens, closes := payloadFraming([]byte(tc.payload))
		if opens != tc.opens || closes != tc.closes {
			t.Errorf("%s: payloadFraming = (%t, %t), want (%t, %t)", tc.name, opens, closes, tc.opens, tc.closes)
		}
	}
}
