package releasetimeline

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestPRNumber(t *testing.T) {
	tests := []struct {
		subject string
		want    int
		ok      bool
	}{
		{"fix(gateway): drop the retry (#123)", 123, true},
		{"feat: x (#7)  ", 7, true},
		{"Merge pull request #45 from acme/branch", 45, true},
		{`Revert "fix a thing (#12)" (#15)`, 15, true},
		{"fix a thing (#12) and more", 0, false},
		{"mentions #99 in prose", 0, false},
		{"fix: closes (#abc)", 0, false},
		{"no number", 0, false},
		{"", 0, false},
		{"huge (#99999999999)", 0, false},
	}
	for _, tc := range tests {
		n, ok := PRNumber(tc.subject)
		if n != tc.want || ok != tc.ok {
			t.Errorf("PRNumber(%q) = %d, %v; want %d, %v", tc.subject, n, ok, tc.want, tc.ok)
		}
	}
}

func TestPRNumbers_OrderAndDedup(t *testing.T) {
	commits := []release.Commit{
		{Subject: "c (#3)"}, {Subject: "plain commit"}, {Subject: "b (#2)"}, {Subject: "c again (#3)"},
	}
	if got := PRNumbers(commits); !reflect.DeepEqual(got, []int{3, 2}) {
		t.Errorf("got %v", got)
	}
	if got := PRNumbers(nil); got != nil {
		t.Errorf("got %v", got)
	}
}
