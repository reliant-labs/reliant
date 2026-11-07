package generator

import (
	"fmt"
	"path"
	"strings"

	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

// A greenfield static site (`forge project new <name> --kind site`) is
// written as the app an adoption would find, and then adopted: a standard
// Next.js static export or Vite SPA at the project root, plus exactly the
// forge.yaml and deploy envs WriteSiteScaffold writes for an existing app.
// So a new site and an adopted one are ONE project shape — the frontend at
// `path = "."`, no Go module — and every later command, the framework memory
// and the deploy/static-site skill describe that one shape.
//
// It is deliberately NOT the app frontend `forge scaffold frontend` writes.
// That tree is wired to a backend: a typed-config proto, Connect transport,
// generated hooks and mocks, auth screens. In a project with no Go module it
// drags in proto/, gen/ and buf, and its first generate fails. A site
// starter carries only what a static site uses, so a site has no proto and
// forge's frontend codegen (which runs only for a project with protos)
// never touches it — exactly as for an adopted app.

// siteStarterKinds are the frontend kinds a greenfield site starts from,
// keyed by the `--frontend-kind` value; each maps to the KCL forge.Frontend
// type and build directory the deploy envs declare.
var siteStarterKinds = map[string]SiteFrontend{
	"nextjs":   {Type: "nextjs", PublicDir: "out"},
	"vite-spa": {Type: "vite", PublicDir: "dist"},
}

// SiteStarterFrontend returns the SiteFrontend a greenfield site of the
// given kind ("nextjs" or "vite-spa") declares under name, or false for a
// kind there is no starter for.
func SiteStarterFrontend(kind, name string) (SiteFrontend, bool) {
	fe, ok := siteStarterKinds[kind]
	fe.Name = name
	return fe, ok
}

// SiteStarterKindNames lists the kinds SiteStarterFrontend accepts, sorted.
func SiteStarterKindNames() []string { return []string{"nextjs", "vite-spa"} }

// siteStarterReused are files a site starter shares with the app frontend
// of the same framework, rendered from the app tree rather than copied into
// the site's: the dev-server half of browser log forwarding and the
// Tailwind PostCSS wiring. One file, so a fix to it reaches both.
var siteStarterReused = map[string][]struct{ from, to string }{
	"vite": {
		{from: "vite-spa/src/lib/devlog-plugin.ts", to: "src/lib/devlog-plugin.ts"},
	},
	"nextjs": {
		{from: "nextjs/src/app/%5F_forge/log/route.ts", to: "src/app/%5F_forge/log/route.ts"},
		{from: "nextjs/postcss.config.mjs", to: "postcss.config.mjs"},
	},
}

// siteStarterData is what the site starter templates render from.
type siteStarterData struct {
	ProjectName string
	// WebRuntimeRange is the @reliantlabs/forge-web-runtime range the
	// starter declares — the same floor every forge frontend declares
	// (webRuntimePublishedRange). The site uses it for installDevLogging.
	WebRuntimeRange string
	// Connect, Protobuf and OTelAPI are the peers the runtime's barrel
	// imports at module top level (interceptors, errors, trace). The
	// runtime marks the first two optional because every forge frontend
	// declares them itself, so a site that imports the barrel must too, or
	// its production build cannot resolve them. Specifiers come from the
	// runtime's own peerDependencies, so they cannot drift from it.
	Connect  string
	Protobuf string
	OTelAPI  string
}

// WriteSiteStarter writes the starter app of a greenfield static site into
// dir (its root): the template tree internal/templates/frontend/site/<type>
// plus the files it reuses from the app frontend of that framework. fe is
// what SiteStarterFrontend returned; WriteSiteScaffold then writes the forge
// files declaring it.
func WriteSiteStarter(dir, projectName string, fe SiteFrontend) error {
	root := path.Join("site", fe.Type)
	files, err := templates.FrontendTemplates().List(root)
	if err != nil {
		return fmt.Errorf("list %s site starter: %w", fe.Type, err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no site starter for frontend type %q", fe.Type)
	}
	data := siteStarterData{
		ProjectName:     projectName,
		WebRuntimeRange: webRuntimePublishedRange,
		Connect:         webRuntimePeer("@connectrpc/connect"),
		Protobuf:        webRuntimePeer("@bufbuild/protobuf"),
		OTelAPI:         webRuntimePeer("@opentelemetry/api"),
	}
	for _, rel := range files {
		content, err := templates.FrontendTemplates().Render(path.Join(root, rel), data)
		if err != nil {
			return fmt.Errorf("render site starter %s: %w", rel, err)
		}
		if err := writeSiteFile(dir, strings.TrimSuffix(rel, ".tmpl"), content); err != nil {
			return err
		}
	}
	for _, f := range siteStarterReused[fe.Type] {
		content, err := templates.FrontendTemplates().Get(f.from)
		if err != nil {
			return fmt.Errorf("read %s: %w", f.from, err)
		}
		if err := writeSiteFile(dir, f.to, content); err != nil {
			return err
		}
	}
	return nil
}

// webRuntimePeer is the specifier the runtime declares for one of its peers.
// A peer the runtime stops declaring is a drifted starter, so it panics
// rather than render an empty version.
func webRuntimePeer(name string) string {
	spec, ok := webruntimepeers.PeerSpec(name)
	if !ok {
		panic("web runtime declares no peer " + name)
	}
	return spec
}
