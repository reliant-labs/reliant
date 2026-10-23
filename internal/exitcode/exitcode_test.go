package exitcode

import (
	"errors"
	"fmt"
	"testing"
)

type coded int

func (c coded) Error() string { return "coded" }
func (c coded) ExitCode() int { return int(c) }

func TestOf(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"plain", errors.New("boom"), 1},
		{"coded 2", coded(2), 2},
		{"coded 8 wrapped", fmt.Errorf("wrap: %w", coded(8)), 8},
		{"joined keeps the coded one", errors.Join(errors.New("write"), coded(2)), 2},
		{"coded 0 is still a failure", coded(0), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Of(tc.err); got != tc.want {
				t.Fatalf("Of = %d, want %d", got, tc.want)
			}
		})
	}
}
