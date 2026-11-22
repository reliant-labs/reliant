package pgtest_test

import (
	"errors"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestFetchErrorLayoutIsFrozen holds FetchError to the four fields it shipped
// with, in order. A caller outside the package may build it positionally, so
// adding any field — exported or not — breaks their build: an incompatible
// change to pkg/ at major 1. This literal stops compiling first.
func TestFetchErrorLayoutIsFrozen(t *testing.T) {
	cause := errors.New("connection refused")
	fe := pgtest.FetchError{"https://repo1.maven.org/maven2/x.jar", 0, 6, cause} //nolint:govet // positional on purpose: it is what this test protects
	if fe.URL == "" || fe.Status != 0 || fe.Attempts != 6 || !errors.Is(&fe, cause) {
		t.Errorf("FetchError = %+v, want its four fields in their original order", fe)
	}
	// It stays comparable, as it was: == on two values compiles.
	same := fe
	if same != fe {
		t.Error("a copy of a FetchError is not equal to it")
	}
}
