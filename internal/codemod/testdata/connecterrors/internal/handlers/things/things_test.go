package things

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
)

// The handler-test shape reliant has 326 of: call the function directly and
// read the code. connect.CodeOf reads a bare svcerr kind as unknown, so the
// codemod rewrites these to svcerr.Code — the code the interceptor sends.
func TestCodes(t *testing.T) {
	if got := connect.CodeOf(invalidArgument()); got != connect.CodeInvalidArgument {
		t.Fatalf("invalidArgument: %v", got)
	}
	if code := connect.CodeOf(notFoundSentence()); code != connect.CodeNotFound {
		t.Fatalf("notFoundSentence: %v", code)
	}
	if connect.CodeOf(nil) != connect.CodeUnknown {
		t.Fatal("nil reads unknown either way")
	}
	// A fixture error built in a test stays as written.
	fixture := connect.NewError(connect.CodeAborted, errors.New("fixture"))
	if connect.CodeOf(fixture) != connect.CodeAborted {
		t.Fatal("fixture")
	}
}
