package testreport_test

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/testreport"
)

// cmd/go reports a test build failure under the test variant's name
// ("p [p.test]") and the package's own fail event separately. pkg/testreport
// folds the two into one package; the skip gate has always listed both, and
// moving it onto the shared parser keeps that output byte for byte.
func TestParse_TestVariantBuildFailureKeepsBothEntries(t *testing.T) {
	in := `{"ImportPath":"example.com/res/broken [example.com/res/broken.test]","Action":"build-output","Output":"broken.go:3: cannot use\n"}
{"ImportPath":"example.com/res/broken [example.com/res/broken.test]","Action":"build-fail"}
{"Action":"start","Package":"example.com/res/broken"}
{"Action":"output","Package":"example.com/res/broken","Output":"FAIL\texample.com/res/broken [build failed]\n"}
{"Action":"fail","Package":"example.com/res/broken","Elapsed":0,"FailedBuild":"example.com/res/broken [example.com/res/broken.test]"}
`
	run, err := testreport.Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Packages) != 2 {
		t.Fatalf("packages = %+v", run.Packages)
	}
	base, variant := run.Packages[0], run.Packages[1]
	if base.Path != "example.com/res/broken" || base.BuildFailed || base.Result != testreport.OutcomeFail || base.Incomplete {
		t.Errorf("base = %+v", base)
	}
	if variant.Path != "example.com/res/broken [example.com/res/broken.test]" || !variant.BuildFailed || variant.Incomplete {
		t.Errorf("variant = %+v", variant)
	}
	if run.Events != 5 || run.Malformed != 0 {
		t.Errorf("events %d malformed %d", run.Events, run.Malformed)
	}
}
