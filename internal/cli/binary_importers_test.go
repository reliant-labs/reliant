package cli

import (
	"slices"
	"strings"
	"testing"
)

// binaryImportersOf splits the packages in listing (ImportPath|Module|Imports
// lines of `go list -deps -f`) that import pkg into those whose module owned
// accepts and the rest.
func binaryImportersOf(listing, pkg string, owned func(module string) bool) (offenders, thirdParty []string) {
	for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) != 3 || !slices.Contains(strings.Split(parts[2], ","), pkg) {
			continue
		}
		if owned(parts[1]) {
			offenders = append(offenders, parts[0])
		} else {
			thirdParty = append(thirdParty, parts[0]+" ("+parts[1]+")")
		}
	}
	return offenders, thirdParty
}

// The e2e guard fails on the project's or forge's own non-test import of
// testing, and only logs a third-party one (prometheus/common v0.73.0's
// expfmt -> pandatix/go-abnf *_fuzz.go), which no scaffold change removes.
func TestBinaryImportersOfJudgesTheImportingModule(t *testing.T) {
	listing := strings.Join([]string{
		"testing||errors,flag,fmt",
		"github.com/pandatix/go-abnf|github.com/pandatix/go-abnf|bytes,testing",
		"github.com/prometheus/common/expfmt|github.com/prometheus/common|github.com/pandatix/go-abnf",
		"example.com/app/internal/testfactory|example.com/app|fmt,testing",
		"github.com/reliant-labs/forge/pkg/tdd|github.com/reliant-labs/forge|testing,testing/fstest",
		"example.com/app/internal/handlers/order|example.com/app|context,testing/fstest",
	}, "\n")
	owned := func(m string) bool { return m == "example.com/app" || m == "github.com/reliant-labs/forge" }
	offenders, thirdParty := binaryImportersOf(listing, "testing", owned)
	if want := []string{"example.com/app/internal/testfactory", "github.com/reliant-labs/forge/pkg/tdd"}; !slices.Equal(offenders, want) {
		t.Errorf("offenders = %v, want %v", offenders, want)
	}
	if want := []string{"github.com/pandatix/go-abnf (github.com/pandatix/go-abnf)"}; !slices.Equal(thirdParty, want) {
		t.Errorf("thirdParty = %v, want %v", thirdParty, want)
	}
}
