package contractkit

import "testing"

// TestMockNotSet_FingerprintLocked locks the exact error format. Tests
// in dogfood projects assert on this substring; if you bump the format
// you must coordinate the change across every consumer.
func TestMockNotSet_FingerprintLocked(t *testing.T) {
	cases := []struct {
		mock, method, want string
	}{
		{"MockService", "Send", "MockService.SendFunc not set"},
		{"MockParser", "ParseConfig", "MockParser.ParseConfigFunc not set"},
		{"MockX", "Y", "MockX.YFunc not set"},
	}
	for _, tc := range cases {
		err := MockNotSet(tc.mock, tc.method)
		if err == nil {
			t.Fatalf("MockNotSet(%q,%q) returned nil", tc.mock, tc.method)
		}
		if err.Error() != tc.want {
			t.Errorf("MockNotSet(%q,%q) = %q, want %q",
				tc.mock, tc.method, err.Error(), tc.want)
		}
	}
}
