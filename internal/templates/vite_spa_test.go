package templates

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestViteSPATemplatesList confirms the composed vite-spa template tree
// (frontend/shared + frontend/shared-web + frontend/vite-spa) carries the
// files the generator depends on. Missing entries here would silently
// produce a half-scaffolded SPA where buf / vite / tanstack-router would
// fail at first run. Asserting on the COMPOSED tree — not on one
// directory — is what keeps a mechanism file moving between roots from
// being a silent scaffold regression.
func TestViteSPATemplatesList(t *testing.T) {
	files, err := ListFrontendTree("vite-spa")
	if err != nil {
		t.Fatalf("list vite-spa template tree: %v", err)
	}

	expected := []string{
		"package.json.tmpl",
		"vite.config.ts.tmpl",
		"tsconfig.json.tmpl",
		"tsconfig.node.json",
		"index.html.tmpl",
		".gitignore",
		"buf.gen.yaml.tmpl",
		"eslint.config.mjs",
		"src/main.tsx",
		"src/App.tsx.tmpl",
		"src/routes.tsx.tmpl",
		"src/index.css",
		"src/vite-env.d.ts",
		"src/stores/ui-store.ts",
		"src/lib/connect.ts.tmpl",
		"src/lib/apiurl_gen.ts.tmpl",
		"src/lib/query-client.ts",
		"src/lib/events.ts",
		"src/lib/event-context.tsx.tmpl",
		"src/lib/search-schemas.ts",
		"src/lib/format-utils.ts",
		"src/lib/auth/provider.ts",
		"src/lib/auth/session-provider.ts.tmpl",
		"src/lib/auth/context.tsx.tmpl",
		"src/hooks/use-api-query.ts",
		"src/hooks/use-api-mutation.ts",
	}

	fileSet := make(map[string]string)
	for _, f := range files {
		fileSet[f.Rel] = f.Path
	}

	for _, e := range expected {
		if _, ok := fileSet[e]; !ok {
			t.Errorf("expected template %s not found in listing", e)
		}
	}

	// React Native must NOT pick up the browser-only root.
	native, err := ListFrontendTree("react-native")
	if err != nil {
		t.Fatalf("list react-native template tree: %v", err)
	}
	for _, f := range native {
		if strings.HasPrefix(f.Path, "shared-web/") {
			t.Errorf("react-native composed a browser-only template: %s", f.Path)
		}
	}
}

// TestViteSPATemplatesRender exercises the full template tree through the
// shared template engine. Any unparseable Go template token, undefined
// data key, or empty rendered output gets flagged before the user hits it.
func TestViteSPATemplatesRender(t *testing.T) {
	data := FrontendTemplateData{
		FrontendName: "myspa",
		ProjectName:  "testproject",
		Platform:     "vite-spa",
		APIURL:       "http://localhost:8080",
		Module:       "example.com/testproject",
	}

	files, err := ListFrontendTree("vite-spa")
	if err != nil {
		t.Fatalf("list vite-spa template tree: %v", err)
	}

	for _, f := range files {
		t.Run(f.Rel, func(t *testing.T) {
			content, err := FrontendTemplates().Render(f.Path, data)
			if err != nil {
				t.Fatalf("render %s: %v", f.Path, err)
			}
			if len(content) == 0 {
				t.Errorf("rendered %s is empty", f.Path)
			}
			// Only the Next.js App Router understands the RSC prologue;
			// Vite's bundler warns on it and the module is a plain client
			// module anyway.
			if strings.HasPrefix(string(content), `"use client"`) {
				t.Errorf("%s rendered a \"use client\" prologue into a Vite SPA", f.Path)
			}
		})
	}

	t.Run("package.json contains vite + react", func(t *testing.T) {
		content, _ := FrontendTemplates().Render("vite-spa/package.json.tmpl", data)
		s := string(content)
		if !strings.Contains(s, `"name": "myspa"`) {
			t.Error("package.json should contain frontend name")
		}
		if !strings.Contains(s, `"vite"`) {
			t.Error("package.json should contain vite dependency")
		}
		if !strings.Contains(s, "@tanstack/react-router") {
			t.Error("package.json should contain @tanstack/react-router")
		}
		if !strings.Contains(s, "@tanstack/react-query") {
			t.Error("package.json should contain @tanstack/react-query")
		}
		if !strings.Contains(s, "@tailwindcss/vite") {
			t.Error("package.json should contain @tailwindcss/vite")
		}
	})

	t.Run("connect.ts uses VITE_API_URL and the apiurl_gen floor", func(t *testing.T) {
		content, _ := FrontendTemplates().Render("vite-spa/src/lib/connect.ts.tmpl", data)
		s := string(content)
		if !strings.Contains(s, "VITE_API_URL") {
			t.Error("connect.ts should reference VITE_API_URL")
		}
		// The dev-URL floor moved out of connect.ts (and the deleted
		// .env.local) into the regenerated apiurl_gen.ts; connect.ts now
		// imports DEV_API_URL from it rather than baking the literal URL.
		if !strings.Contains(s, "DEV_API_URL") {
			t.Error("connect.ts should import DEV_API_URL from apiurl_gen")
		}
		gen, _ := FrontendTemplates().Render("vite-spa/src/lib/apiurl_gen.ts.tmpl", data)
		if !strings.Contains(string(gen), "http://localhost:8080") {
			t.Error("apiurl_gen.ts should contain the rendered dev API URL")
		}
	})

	t.Run("routes.tsx wires tanstack-router", func(t *testing.T) {
		content, _ := FrontendTemplates().Render("vite-spa/src/routes.tsx.tmpl", data)
		s := string(content)
		if !strings.Contains(s, "createRouter") {
			t.Error("routes.tsx should reference createRouter")
		}
		if !strings.Contains(s, "createRootRoute") {
			t.Error("routes.tsx should reference createRootRoute")
		}
		if !strings.Contains(s, "FORGE-ROUTES: BEGIN") {
			t.Error("routes.tsx should contain the FORGE-ROUTES marker block for page-generator integration")
		}
	})

	t.Run("main.tsx mounts the router", func(t *testing.T) {
		content, _ := FrontendTemplates().Render("vite-spa/src/main.tsx", data)
		s := string(content)
		if !strings.Contains(s, "<Providers />") {
			t.Error("main.tsx should mount the provider stack from ./providers")
		}
		if !strings.Contains(s, "createRoot") {
			t.Error("main.tsx should call createRoot")
		}
		// The stack lives in its own module: a component defined in the entry
		// point has no export, which react-refresh/only-export-components flags.
		if strings.Contains(s, "function ") {
			t.Error("main.tsx should define no component of its own")
		}
		providers, _ := FrontendTemplates().Render("vite-spa/src/providers.tsx", data)
		if !strings.Contains(string(providers), "<RouterProvider router={router} />") {
			t.Error("providers.tsx should mount RouterProvider")
		}
	})
}

// TestViteSPAPageTemplatesRender confirms the vite-spa-pages templates
// render against PageTemplateData (the same shape used for nextjs pages).
// This is the first-line guard that the tanstack-router rewrite of the
// page templates parses cleanly.
func TestViteSPAPageTemplatesRender(t *testing.T) {
	pages := []string{
		"list-page.tsx.tmpl",
		"detail-page.tsx.tmpl",
		"create-page.tsx.tmpl",
		"edit-page.tsx.tmpl",
	}

	// Page templates consume codegen.PageTemplateData, but at the templates
	// package we can't import codegen (cycle). Use the same field surface
	// inline via a map literal — Go template lookups are field-name based
	// for both structs and `map[string]any`.
	data := map[string]any{
		"EntityName":         "Task",
		"EntityNamePlural":   "Tasks",
		"EntitySlug":         "tasks",
		"ServiceName":        "TaskService",
		"ServiceNameCamel":   "taskService",
		"HooksImportPath":    "@/hooks/task-service-hooks",
		"TypesImportPath":    "@/gen/services/tasks/v1/tasks_pb",
		"ListRPC":            "ListTasks",
		"GetRPC":             "GetTask",
		"CreateRPC":          "CreateTask",
		"UpdateRPC":          "UpdateTask",
		"DeleteRPC":          "DeleteTask",
		"HasList":            true,
		"HasGet":             true,
		"HasCreate":          true,
		"HasUpdate":          true,
		"HasDelete":          true,
		"CreateFields":       []any{},
		"UpdateFields":       []any{},
		"ListResponseType":   "ListTasksResponse",
		"GetResponseType":    "GetTaskResponse",
		"CreateRequestType":  "CreateTaskRequest",
		"CreateResponseType": "CreateTaskResponse",
		"UpdateRequestType":  "UpdateTaskRequest",
		"GetRequestType":     "GetTaskRequest",
		"DeleteRequestType":  "DeleteTaskRequest",
	}

	for _, p := range pages {
		p := p
		t.Run(p, func(t *testing.T) {
			content, err := FrontendTemplates().Render(filepath.Join("vite-spa-pages", p), data)
			if err != nil {
				t.Fatalf("render %s: %v", p, err)
			}
			if len(content) == 0 {
				t.Errorf("rendered %s is empty", p)
			}
			// Each page must reach for tanstack-router primitives — that's
			// the whole point of the vite-spa variant. Catch regressions
			// where someone accidentally ports a next/* import in.
			s := string(content)
			if strings.Contains(s, "next/navigation") || strings.Contains(s, "next/link") {
				t.Errorf("vite-spa page %s must not import next/* (got next/* reference)", p)
			}
		})
	}
}

// TestFrontendShellsLoadRuntimeConfigBeforeTheBundle pins that BOTH scaffolded
// web shells load the runtime config document (config.js) with a classic,
// blocking script in <head>, before any module script. The generated
// src/lib/config_gen.ts reads window.__FORGE_CONFIG__ synchronously. A shell
// that never loads the file makes every per-env value silently fall back to
// its schema default. That is what the Vite scaffold did: its index.html had
// no config.js tag at all, so a deployed Vite app called whatever API origin
// the default named.
func TestFrontendShellsLoadRuntimeConfigBeforeTheBundle(t *testing.T) {
	vite, err := FrontendTemplates().Render("vite-spa/index.html.tmpl", FrontendTemplateData{FrontendName: "web", ProjectName: "acme", Platform: "vite-spa"})
	if err != nil {
		t.Fatal(err)
	}
	v := string(vite)
	tag := `<script src="%BASE_URL%config.js"></script>`
	i, j := strings.Index(v, tag), strings.Index(v, `<script type="module"`)
	if i < 0 {
		t.Fatalf("vite-spa index.html does not load %s (honouring Vite's base):\n%s", tag, v)
	}
	if head := strings.Index(v, "</head>"); i > head || j < i {
		t.Errorf("config.js must load in <head>, before the module bundle (config at %d, </head> at %d, module at %d)", i, head, j)
	}

	next, err := FrontendTemplates().Render("nextjs/src/app/layout.tsx.tmpl", FrontendTemplateData{FrontendName: "web", ProjectName: "acme", Platform: "nextjs"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(next), `<script src={joinBasePath("/config.js")} />`) {
		t.Error("nextjs layout no longer loads config.js through joinBasePath")
	}
}
