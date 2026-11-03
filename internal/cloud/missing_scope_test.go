package cloud

import "testing"

func TestErrorMissingScope(t *testing.T) {
	for _, tc := range []struct {
		status int
		msg    string
		want   string
		ok     bool
	}{
		{403, "this credential does not carry the db:query scope", "db:query", true},
		{403, "requires the obs:triage scope", "obs:triage", true},
		{403, "querying this environment's database is disabled by its data_access policy", "", false},
		{403, "you do not hold the db:query grant in this organization", "", false},
		{401, "missing the db:query scope", "", false},
	} {
		got, ok := (&Error{HTTPStatus: tc.status, Message: tc.msg}).MissingScope()
		if got != tc.want || ok != tc.ok {
			t.Errorf("%d %q: got (%q, %v), want (%q, %v)", tc.status, tc.msg, got, ok, tc.want, tc.ok)
		}
	}
	if _, ok := (*Error)(nil).MissingScope(); ok {
		t.Error("a nil error names no scope")
	}
}
