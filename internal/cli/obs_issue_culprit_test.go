package cli

import (
	"testing"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// forge#2235: an issue grouped before the culprit named the rpc still shows
// the rpc, read from its newest event, rather than forge's interceptor.
func TestObsIssueShowsTheRPCForACulpritInForgesInterceptor(t *testing.T) {
	const interceptor = "github.com/reliant-labs/forge/pkg/observe.(*loggingInterceptor).log"
	const stored = "/go/pkg/mod/github.com/reliant-labs/forge@v1.6.0/pkg/observe/interceptors.go in " + interceptor
	const procedure = "/customers.v1.CustomerService/ArchiveCustomer"
	event := func(attrs map[string]string) *obsbackend.IssueEvent { return &obsbackend.IssueEvent{Attributes: attrs} }
	forgeLogged := event(map[string]string{obsquery.LogKeyCodeFunction: interceptor, obsquery.LogKeyProcedure: procedure})

	for name, tc := range map[string]struct {
		culprit string
		latest  *obsbackend.IssueEvent
		want    string
	}{
		"forge's interceptor, newest event names the rpc": {culprit: stored, latest: forgeLogged, want: procedure},
		"forge's interceptor, no event to read":           {culprit: stored, want: stored},
		"forge's interceptor, newest event has no procedure": {culprit: stored, want: stored,
			latest: event(map[string]string{obsquery.LogKeyCodeFunction: interceptor})},
		"an application culprit is left alone": {culprit: "internal/store/db.go in store.Exec", latest: forgeLogged,
			want: "internal/store/db.go in store.Exec"},
		"already the procedure": {culprit: procedure, latest: forgeLogged, want: procedure},
	} {
		t.Run(name, func(t *testing.T) {
			if got := displayCulprit(tc.culprit, tc.latest); got != tc.want {
				t.Errorf("displayCulprit = %q, want %q", got, tc.want)
			}
		})
	}
}
