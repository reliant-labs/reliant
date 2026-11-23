package templates

import (
	"strings"
	"testing"
)

// TestFrontendPackageJson_NoWorkspaceDepsByDefault asserts the default
// (workspaces=false) rendering of each frontend's package.json contains
// no `workspace:*` references. This is the byte-stability load-bearing
// test for projects that haven't opted into pnpm-workspaces.
func TestFrontendPackageJson_NoWorkspaceDepsByDefault(t *testing.T) {
	data := FrontendTemplateData{
		FrontendName: "web",
		ProjectName:  "myapp",
		APIURL:       "http://localhost:8080",
		Module:       "example.com/myapp",
		// Workspaces left zero — this is the default snapshot mode.
	}

	for _, kind := range []string{"nextjs", "react-native", "vite-spa"} {
		t.Run(kind, func(t *testing.T) {
			out, err := FrontendTemplates().Render(kind+"/package.json.tmpl", data)
			if err != nil {
				t.Fatalf("render %s/package.json.tmpl: %v", kind, err)
			}
			if strings.Contains(string(out), "workspace:*") {
				t.Errorf("%s/package.json should not contain workspace:* by default, got:\n%s", kind, out)
			}
			if strings.Contains(string(out), "@myapp/api") {
				t.Errorf("%s/package.json should not contain @myapp/api by default, got:\n%s", kind, out)
			}
		})
	}
}

// TestFrontendPackageJson_WorkspaceDepsWhenEnabled asserts that the
// rendered package.json declares "workspace:*" deps on the project's
// @<scope>/api and @<scope>/hooks workspaces when Workspaces is true.
// This is what lets `pnpm install` link the shared packages into each
// frontend's node_modules.
func TestFrontendPackageJson_WorkspaceDepsWhenEnabled(t *testing.T) {
	data := FrontendTemplateData{
		FrontendName: "web",
		ProjectName:  "myapp",
		APIURL:       "http://localhost:8080",
		Module:       "example.com/myapp",
		Workspaces:   true,
		APIPackage:   "@myapp/api",
		HooksPackage: "@myapp/hooks",
	}

	for _, kind := range []string{"nextjs", "react-native", "vite-spa"} {
		t.Run(kind, func(t *testing.T) {
			out, err := FrontendTemplates().Render(kind+"/package.json.tmpl", data)
			if err != nil {
				t.Fatalf("render %s/package.json.tmpl: %v", kind, err)
			}
			s := string(out)
			if !strings.Contains(s, `"@myapp/api": "workspace:*"`) {
				t.Errorf("%s/package.json should declare @myapp/api workspace dep, got:\n%s", kind, s)
			}
			if !strings.Contains(s, `"@myapp/hooks": "workspace:*"`) {
				t.Errorf("%s/package.json should declare @myapp/hooks workspace dep, got:\n%s", kind, s)
			}
		})
	}
}

// TestReactNativePackageJson_UINativeWorkspaceDep asserts the React
// Native package.json declares "@<scope>/ui-native": "workspace:*"
// when Workspaces is on. This is the bridge between the workspace
// emit (packages/ui-native/) and the frontend that consumes it.
func TestReactNativePackageJson_UINativeWorkspaceDep(t *testing.T) {
	data := FrontendTemplateData{
		FrontendName:    "mobile",
		ProjectName:     "myapp",
		APIURL:          "http://localhost:8080",
		Module:          "example.com/myapp",
		Workspaces:      true,
		APIPackage:      "@myapp/api",
		HooksPackage:    "@myapp/hooks",
		UINativePackage: "@myapp/ui-native",
	}
	out, err := FrontendTemplates().Render("react-native/package.json.tmpl", data)
	if err != nil {
		t.Fatalf("render react-native/package.json.tmpl: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"@myapp/ui-native": "workspace:*"`) {
		t.Errorf("react-native/package.json should declare @myapp/ui-native workspace dep, got:\n%s", s)
	}
	if !strings.Contains(s, "react-native-safe-area-context") {
		t.Errorf("react-native/package.json should include react-native-safe-area-context dep (SafeAreaView primitive depends on it), got:\n%s", s)
	}
}

// TestReactNativePackageJson_NoUINativeByDefault asserts no
// `ui-native` reference appears when workspaces is off. Snapshot
// stability for projects that haven't opted in.
func TestReactNativePackageJson_NoUINativeByDefault(t *testing.T) {
	data := FrontendTemplateData{
		FrontendName: "mobile",
		ProjectName:  "myapp",
		APIURL:       "http://localhost:8080",
		Module:       "example.com/myapp",
	}
	out, err := FrontendTemplates().Render("react-native/package.json.tmpl", data)
	if err != nil {
		t.Fatalf("render react-native/package.json.tmpl: %v", err)
	}
	if strings.Contains(string(out), "ui-native") {
		t.Errorf("react-native/package.json should not mention ui-native by default, got:\n%s", out)
	}
}

// TestReactNativeIndexScreen_UsesUINativeWhenEnabled asserts the
// scaffolded home screen imports the ui-native primitives when
// workspaces is on — gives the user a working visual reference for
// the package out-of-the-box rather than a bare-RN starting point.
func TestReactNativeIndexScreen_UsesUINativeWhenEnabled(t *testing.T) {
	data := FrontendTemplateData{
		FrontendName:    "mobile",
		ProjectName:     "myapp",
		APIURL:          "http://localhost:8080",
		Module:          "example.com/myapp",
		Workspaces:      true,
		APIPackage:      "@myapp/api",
		HooksPackage:    "@myapp/hooks",
		UINativePackage: "@myapp/ui-native",
	}
	out, err := FrontendTemplates().Render("react-native/app/index.tsx.tmpl", data)
	if err != nil {
		t.Fatalf("render react-native/app/index.tsx.tmpl: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `from "@myapp/ui-native"`) {
		t.Errorf("workspaces home screen should import from @myapp/ui-native, got:\n%s", s)
	}
	for _, sym := range []string{"Button", "Stack", "Text", "Card", "SafeAreaView"} {
		if !strings.Contains(s, sym) {
			t.Errorf("workspaces home screen should reference %s, got:\n%s", sym, s)
		}
	}
}

// TestReactNativeIndexScreen_BareWhenDisabled asserts the default
// (workspaces=off) home screen stays the pure-RN reference that
// existed before this change — byte-stable for non-opted-in projects.
func TestReactNativeIndexScreen_BareWhenDisabled(t *testing.T) {
	data := FrontendTemplateData{
		FrontendName: "mobile",
		ProjectName:  "myapp",
		APIURL:       "http://localhost:8080",
		Module:       "example.com/myapp",
	}
	out, err := FrontendTemplates().Render("react-native/app/index.tsx.tmpl", data)
	if err != nil {
		t.Fatalf("render react-native/app/index.tsx.tmpl: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "ui-native") {
		t.Errorf("default home screen should not reference ui-native, got:\n%s", s)
	}
	if !strings.Contains(s, `from "react-native"`) {
		t.Errorf("default home screen should still import from react-native, got:\n%s", s)
	}
}

// TestFrontendConnect_ImportsWorkspaceHooks asserts that when
// Workspaces is true the rendered connect.ts.tmpl wires
// setApiTransport from the shared hooks package — without that the
// generated hooks have no way to reach the network.
func TestFrontendConnect_ImportsWorkspaceHooks(t *testing.T) {
	data := FrontendTemplateData{
		FrontendName: "web",
		ProjectName:  "myapp",
		APIURL:       "http://localhost:8080",
		Module:       "example.com/myapp",
		Workspaces:   true,
		APIPackage:   "@myapp/api",
		HooksPackage: "@myapp/hooks",
	}

	for _, kind := range []string{"nextjs", "react-native", "vite-spa"} {
		t.Run(kind, func(t *testing.T) {
			out, err := FrontendTemplates().Render(kind+"/src/lib/connect.ts.tmpl", data)
			if err != nil {
				t.Fatalf("render %s/src/lib/connect.ts.tmpl: %v", kind, err)
			}
			s := string(out)
			if !strings.Contains(s, `import { setApiTransport } from "@myapp/hooks"`) {
				t.Errorf("%s/connect.ts should import setApiTransport from @myapp/hooks, got:\n%s", kind, s)
			}
			if !strings.Contains(s, "setApiTransport(transport)") {
				t.Errorf("%s/connect.ts should call setApiTransport(transport), got:\n%s", kind, s)
			}
		})
	}
}

// TestFrontendConnect_NoWorkspaceImportsByDefault asserts the default
// rendering of connect.ts.tmpl contains no setApiTransport call. Pins
// the snapshot invariant: projects without workspaces opt-in see the
// historic connect.ts output unchanged.
func TestFrontendConnect_NoWorkspaceImportsByDefault(t *testing.T) {
	data := FrontendTemplateData{
		FrontendName: "web",
		ProjectName:  "myapp",
		APIURL:       "http://localhost:8080",
		Module:       "example.com/myapp",
	}

	for _, kind := range []string{"nextjs", "react-native", "vite-spa"} {
		t.Run(kind, func(t *testing.T) {
			out, err := FrontendTemplates().Render(kind+"/src/lib/connect.ts.tmpl", data)
			if err != nil {
				t.Fatalf("render %s/src/lib/connect.ts.tmpl: %v", kind, err)
			}
			s := string(out)
			if strings.Contains(s, "setApiTransport") {
				t.Errorf("%s/connect.ts should not call setApiTransport by default, got:\n%s", kind, s)
			}
		})
	}
}
