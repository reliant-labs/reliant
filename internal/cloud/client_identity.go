package cloud

import (
	"context"
	"net/http"
	"os"
	"runtime"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/pkg/observe/cliev"
)

// ClientHeader names the coding agent (if any) and CI runner driving this
// forge, on every control-plane call, e.g. "harness=claude; ci=false". The
// control plane records it on a claimable deploy, so the agent channel's
// funnel can be broken down by the agent that drove it.
//
// A claimable deploy also names its shape, once forge has rendered the env
// (WithDeployShape): "harness=claude; ci=false; template=vite; auth=none".
// Every other run sends the first two keys only. A control plane that does
// not know a key ignores it.
//
// PRIVACY. The value is built only from fixed vocabularies: an agent name from
// buildinfo.DetectAgent ("none" when no agent is detected), a boolean, and on
// a claimable deploy one of the Template* and Auth* constants. It never
// carries an environment variable's value, a path, a hostname, a project,
// workload or user name, so it is safe on an unauthenticated request.
const ClientHeader = "X-Forge-Client"

// The template= vocabulary: what kind of app a claimable deploy ships.
const (
	// TemplateForgeNative: the env deploys a server or worker forge builds
	// from Go (forge.GoBuild), with or without a frontend.
	TemplateForgeNative = "forge-native"
	// TemplateDockerfileApp: the env deploys a workload built from the
	// user's own Dockerfile, or a prebuilt image (an adopted app), and no
	// server forge builds from Go.
	TemplateDockerfileApp = "dockerfile-app"
	// TemplateNextjsStatic: a static site only, a Next.js export.
	TemplateNextjsStatic = "nextjs-static"
	// TemplateVite: a static site only, a Vite build.
	TemplateVite = "vite"
	// TemplateOther: anything else (an Expo app, a shell build, a mix).
	TemplateOther = "other"
)

// The auth= vocabulary: how the deployed app signs its users in.
const (
	// AuthManaged: the env declares a forge.ManagedIdentity.
	AuthManaged = "managed"
	// AuthNone: it declares none.
	AuthNone = "none"
)

// DeployShape is what a claimable deploy says about itself in ClientHeader:
// one Template* and one Auth* constant.
type DeployShape struct {
	Template string
	Auth     string
}

type deployShapeKey struct{}

// WithDeployShape makes control-plane calls using the returned context name
// this deploy's shape. Other deployments and unrelated calls are unaffected.
// Values outside the vocabulary become "other", never free text on the wire.
func WithDeployShape(ctx context.Context, s DeployShape) context.Context {
	return context.WithValue(ctx, deployShapeKey{}, DeployShape{
		Template: inVocabulary(s.Template, TemplateForgeNative, TemplateDockerfileApp, TemplateNextjsStatic, TemplateVite, TemplateOther),
		Auth:     inVocabulary(s.Auth, AuthManaged, AuthNone),
	})
}

func deployShapeFromContext(ctx context.Context) DeployShape {
	s, _ := ctx.Value(deployShapeKey{}).(DeployShape)
	return s
}

func inVocabulary(v string, allowed ...string) string {
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return "other" // the control plane's word for a value outside the vocabulary
}

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
	return clientHeaderValue(os.Getenv, buildinfo.IsCI(), DeployShape{})
}

func clientHeaderValue(getenv func(string) string, ci bool, shape DeployShape) string {
	harness, _ := buildinfo.DetectAgent(getenv)
	if harness == "" {
		harness = HarnessNone
	}
	ciValue := "false"
	if ci {
		ciValue = "true"
	}
	v := "harness=" + harness + "; ci=" + ciValue
	if shape.Template != "" {
		v += "; template=" + shape.Template + "; auth=" + shape.Auth
	}
	return v
}

// setClientIdentity stamps the headers that identify this client on a request
// to the control plane. Every request forge sends the control plane goes
// through it: Connect calls (Client.call), the OAuth token exchange at login
// (identifiedDoer) and the identity revoke at logout.
func setClientIdentity(ctx context.Context, h http.Header) {
	h.Set("User-Agent", UserAgent())
	h.Set(ClientHeader, clientHeaderValue(os.Getenv, buildinfo.IsCI(), deployShapeFromContext(ctx)))
	setInstallIdentity(h, cliev.Default())
}

// InstallIDHeader carries this machine's anonymous install id (forge's
// Events, pkg/observe/cliev) so the control plane's events about this
// install (a claimable deploy, a CLI login) stitch to its first run.
const InstallIDHeader = "X-Forge-Install-Id"

// AttributionHeader carries where the install came from (the install page's
// rid and utm_*) and its install id, in the form the control plane reads for
// first-touch attribution (control-plane internal/signupattr).
const AttributionHeader = "X-Reliant-Attribution"

// setInstallIdentity stamps the install id and attribution, only while
// forge's telemetry may transmit (cliev Client.Transmits): DO_NOT_TRACK,
// FORGE_TELEMETRY=off, CI and `forge telemetry off` all leave both out, and so
// does a quiet first run until a later run has shown the telemetry notice.
func setInstallIdentity(h http.Header, c *cliev.Client) {
	if !c.Transmits() || c.InstallID() == "" {
		return
	}
	h.Set(InstallIDHeader, c.InstallID())
	q := c.InstallAttribution()
	q.Set("install_id", c.InstallID())
	h.Set(AttributionHeader, q.Encode())
}

// identifiedDoer is an oauth2.HTTPDoer that stamps setClientIdentity on each
// request before handing it to next.
type identifiedDoer struct{ next *http.Client }

func (d identifiedDoer) Do(req *http.Request) (*http.Response, error) {
	setClientIdentity(req.Context(), req.Header)
	return d.next.Do(req)
}
