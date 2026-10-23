package templates

import (
	"strings"
	"testing"
)

// TestReactNativeTemplatesList asserts on the COMPOSED react-native tree
// (frontend/shared + frontend/react-native), so a mechanism file moving
// between roots can't silently drop out of the Expo scaffold.
func TestReactNativeTemplatesList(t *testing.T) {
	files, err := ListFrontendTree("react-native")
	if err != nil {
		t.Fatalf("list react-native template tree: %v", err)
	}

	expected := []string{
		"package.json.tmpl",
		"app.json.tmpl",
		"tsconfig.json",
		"babel.config.js",
		".gitignore",
		"buf.gen.yaml.tmpl",
		"src/lib/connect.ts.tmpl",
		"src/lib/apiurl_gen.ts.tmpl",
		"src/lib/query-client.ts",
		"src/hooks/use-api-query.ts",
		"src/hooks/use-api-mutation.ts",
		"app/_layout.tsx.tmpl",
		"app/index.tsx.tmpl",
		"src/lib/events.ts",
		"src/lib/event-context.tsx.tmpl",
		"src/lib/auth/context.tsx.tmpl",
		"src/lib/auth/provider.ts",
		"src/lib/auth/session-provider.ts.tmpl",
		"src/lib/format-utils.ts",
		"src/hooks/use-query-resource.ts",
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
}

func TestReactNativeTemplatesRender(t *testing.T) {
	data := FrontendTemplateData{
		FrontendName: "myapp",
		ProjectName:  "testproject",
		Platform:     "react-native",
		APIURL:       "http://localhost:8080",
		Module:       "example.com/testproject",
	}

	files, err := ListFrontendTree("react-native")
	if err != nil {
		t.Fatalf("list react-native template tree: %v", err)
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
			// React Native has no React Server Components; the Next.js
			// prologue must never reach the Expo bundle.
			if strings.HasPrefix(string(content), `"use client"`) {
				t.Errorf("%s rendered a \"use client\" prologue into a React Native app", f.Path)
			}
		})
	}

	// Verify specific template outputs
	t.Run("package.json contains expo", func(t *testing.T) {
		content, _ := FrontendTemplates().Render("react-native/package.json.tmpl", data)
		s := string(content)
		if !strings.Contains(s, `"name": "myapp"`) {
			t.Error("package.json should contain frontend name")
		}
		if !strings.Contains(s, "expo") {
			t.Error("package.json should contain expo dependency")
		}
	})

	t.Run("connect.ts uses EXPO_PUBLIC_API_URL and the apiurl_gen floor", func(t *testing.T) {
		content, _ := FrontendTemplates().Render("react-native/src/lib/connect.ts.tmpl", data)
		s := string(content)
		if !strings.Contains(s, "EXPO_PUBLIC_API_URL") {
			t.Error("connect.ts should reference EXPO_PUBLIC_API_URL")
		}
		// The dev-URL floor moved out of connect.ts (and the deleted
		// .env.local) into the regenerated apiurl_gen.ts; connect.ts now
		// imports DEV_API_URL from it rather than baking the literal URL.
		if !strings.Contains(s, "DEV_API_URL") {
			t.Error("connect.ts should import DEV_API_URL from apiurl_gen")
		}
		gen, _ := FrontendTemplates().Render("react-native/src/lib/apiurl_gen.ts.tmpl", data)
		if !strings.Contains(string(gen), "http://localhost:8080") {
			t.Error("apiurl_gen.ts should contain the rendered dev API URL")
		}
	})

	t.Run("layout contains proper JSX", func(t *testing.T) {
		content, _ := FrontendTemplates().Render("react-native/app/_layout.tsx.tmpl", data)
		s := string(content)
		if !strings.Contains(s, "myapp") {
			t.Error("_layout.tsx should contain rendered frontend name")
		}
		if !strings.Contains(s, "QueryClientProvider") {
			t.Error("_layout.tsx should contain QueryClientProvider")
		}
		// Verify JSX double braces are properly rendered (not Go template artifacts)
		if !strings.Contains(s, `options={{ title:`) {
			t.Errorf("_layout.tsx should have JSX options={{...}}, got:\n%s", s)
		}
	})
}
