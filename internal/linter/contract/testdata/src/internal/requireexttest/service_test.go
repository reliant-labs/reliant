package requireexttest_test

import "testing"

// External test package: Test* funcs are not part of the package's API
// surface, so the analyzer must not require a contract.go on the
// `<name>_test` package itself.
func TestDoWork(t *testing.T) {
	_ = t
}

// Test helper structs with exported methods are a common pattern in
// external test packages (mocks, fixtures, builders). Because the
// `_test` package is not API surface, the require-contract rule must
// skip it entirely — even when it has exported methods. Otherwise users
// are forced into the internal-test form to avoid spurious findings,
// losing the API-boundary discipline of black-box tests.
type fakeService struct{}

func (f *fakeService) DoWork() error    { return nil }
func (f *fakeService) HelperOnly() bool { return true }
