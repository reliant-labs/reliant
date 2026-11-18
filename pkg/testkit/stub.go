package testkit

import "fmt"

// StubNotConfigured is what a forge-synthesized test stub returns, as its
// error result, from a method the test never programmed.
//
// # The stubs
//
// The generated test harness fills every required interface-typed Deps
// field so NewTest<Service>(t) constructs without the test wiring each
// collaborator by hand. Where forge already generates a Mock<Role> for the
// field's interface (a mock_gen.go), the harness installs that mock and
// this error is not involved: an unset mock method returns
// contractkit.MockNotSet. For an interface forge mocks nowhere — a
// third-party or standard-library one, or a handler-local one outside a
// forge:deps deps.go — it synthesizes a Stub<Field>, a programmable double: a
// <Method>Func field per method, plus an embedded contractkit.Recorder.
// A test that drives a handler through the dependency sets the Func
// fields for the methods it reaches.
//
// # Why an unprogrammed method refuses instead of answering nil
//
// Any method whose last result is `error` returns this error (with zero
// values beside it) until its Func is set. A nil error is not an empty
// answer, it is an affirmative claim that the call SUCCEEDED, and a stub
// that did nothing has not earned it:
//
//   - From a lookup, (nil, nil) means "found" — Go's convention is that a
//     nil error makes the value valid — so `chat, err := repo.GetChat(..)`
//     followed by `chat.UserID` dereferences nil. Worse, a handler's
//     not-found and failure branches became unreachable through the stub,
//     because it could only ever say "found". Every reliant P3b lane
//     hand-wrote a fake to get past it.
//   - From a policy check, nil means PERMITTED. A dogfood run wrote a test
//     asserting a caller without the required role could not obtain an
//     upload URL; it failed, because the stub had authorized the call. The
//     application's own authorization code was correct and the harness had
//     switched it off, silently.
//
// A method with no error result has no channel to refuse on and returns
// its zero values.
//
// # The same default forge already makes for mocks
//
// contractkit.MockNotSet is this error's twin: a forge-generated
// MockService returns it when a method is invoked whose Func field the
// test never assigned. Both lead with the same "<Double>.<Method>Func not
// set" fingerprint, so an assertion on that text holds for either double.
//
// The pair is deliberately NOT one function. contractkit is imported by
// generated production-adjacent mock code; testkit is the test-harness
// library. Sharing would couple them for a one-line format string.
//
// # What a test does about it
//
// Program the method, or supply a real implementation, through the
// harness's With<Service>Deps option:
//
//	repo := &questions.StubRepo{
//	    GetChatFunc: func(context.Context, string) (*db.Chat, error) {
//	        return nil, db.ErrNotFound
//	    },
//	}
//	svc := questions.NewTestQuestions(t, questions.WithQuestionsDeps(questions.Deps{Repo: repo}))
//
// The error names the stub type and the method, so a test that trips it
// is told precisely which collaborator to configure.
func StubNotConfigured(stubType, method string) error {
	return fmt.Errorf("%s.%sFunc not set: forge test stub not configured — an unprogrammed stub never "+
		"answers nil, because a nil error claims the call succeeded (for a lookup, \"found\"; for a "+
		"policy check, ALLOW). Set %sFunc on the stub, or supply a real implementation for this "+
		"dependency via the generated With<Service>Deps option",
		stubType, method, method)
}
