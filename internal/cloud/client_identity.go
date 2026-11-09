package cloud

import (
	"net/http"
	"os"
	"runtime"

	"github.com/reliant-labs/forge/internal/buildinfo"
)

// ClientHeader names the coding agent (if any) and CI runner driving this
// forge, on every control-plane call, e.g. "harness=claude; ci=false". The
// control plane records it on a claimable deploy, so the agent channel's
// funnel can be broken down by the agent that drove it.
//
// PRIVACY. The value is built only from fixed vocabularies: an agent name from
// buildinfo.DetectAgent ("none" when no agent is detected) and a boolean. It
// never carries an environment variable's value, a path, a hostname or a user
// name, so it is safe on an unauthenticated request.
const ClientHeader = "X-Forge-Client"

// HarnessNone is the harness reported when no coding agent is detected.
const HarnessNone = "none"

// UserAgent is the User-Agent forge sends the control plane:
// "forge/<version> (<os>/<arch>)". It replaces Go's default
// "Go-http-client/2.0", which named neither the tool nor its release.
func UserAgent() string {
	return "forge/" + buildinfo.Version() + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

// ClientHeaderValue is this process's X-Forge-Client value.
func ClientHeaderValue() string {
	return clientHeaderValue(os.Getenv, buildinfo.IsCI())
}

func clientHeaderValue(getenv func(string) string, ci bool) string {
	harness, _ := buildinfo.DetectAgent(getenv)
	if harness == "" {
		harness = HarnessNone
	}
	ciValue := "false"
	if ci {
		ciValue = "true"
	}
	return "harness=" + harness + "; ci=" + ciValue
}

// setClientIdentity stamps the headers that identify this client on a request
// to the control plane. Every request forge sends the control plane goes
// through it: Connect calls (Client.call), the OAuth token exchange at login
// (identifiedDoer) and the identity revoke at logout.
func setClientIdentity(h http.Header) {
	h.Set("User-Agent", UserAgent())
	h.Set(ClientHeader, ClientHeaderValue())
}

// identifiedDoer is an oauth2.HTTPDoer that stamps setClientIdentity on each
// request before handing it to next.
type identifiedDoer struct{ next *http.Client }

func (d identifiedDoer) Do(req *http.Request) (*http.Response, error) {
	setClientIdentity(req.Header)
	return d.next.Do(req)
}
