package phantom

import "testing"

// TestPrimaryPort is the FALSE GREEN this guard exists to expose: it hand-builds
// the one data shape production never produces, so it proves a lookup works on
// data that cannot occur.
func TestPrimaryPort(t *testing.T) {
	c := Component{Name: "api", Ports: map[string]int{"http": 8080}}
	if got := c.PrimaryPort(); got != 8080 {
		t.Fatalf("PrimaryPort() = %d, want 8080", got)
	}
}
