package templates

import (
	"path"
	"sort"
)

// The frontend template tree is composed, not copied.
//
// A scaffolded frontend is assembled from an ORDERED list of embedded
// roots under internal/templates/frontend/:
//
//	shared/      generic mechanism modules — the event bus, the React Query
//	             client, the Connect hook wrappers, the auth provider seam,
//	             the formatting helpers. Rendered into ALL frontend kinds.
//	shared-web/  the same idea for browser targets only: the DOM component
//	             wrappers, the CSS-var theme provider, the Vitest harness,
//	             the sidebar/command-palette UI store. Next.js + Vite.
//	<kind>/      what genuinely differs per platform: the app shell, the
//	             router, the transport wiring, package.json, tsconfig,
//	             lint config.
//
// Later roots win, so a kind CAN shadow a shared file by carrying its own
// copy at the same relative path. That is an escape hatch, not a pattern:
// a file worth shadowing is usually a file that belongs in <kind>/ outright.
//
// Small, single-concern platform deltas inside a shared file are expressed
// with a template gate on FrontendTemplateData.Platform rather than by
// forking the file — today the only one is the Next.js App Router's
// `"use client"` prologue.

// FrontendTreeFile is one file in the composed template tree for a
// frontend kind.
type FrontendTreeFile struct {
	// Rel is the path relative to the generated frontend root, with any
	// .tmpl suffix still attached (e.g. "src/lib/events.ts",
	// "src/lib/auth/context.tsx.tmpl").
	Rel string
	// Path is the path within the frontend template category, ready to
	// hand to FrontendTemplates().Render (e.g. "shared/src/lib/events.ts").
	Path string
}

// FrontendTemplateRoots returns the ordered embedded roots composing the
// template tree for a frontend kind ("nextjs", "vite-spa",
// "react-native"). Later roots override earlier ones.
//
// An unrecognised kind composes from that directory alone, so a caller
// naming a tree this function has not been taught about still gets that
// tree's own files instead of silently getting nothing.
func FrontendTemplateRoots(kind string) []string {
	switch kind {
	case "nextjs", "vite-spa":
		return []string{"shared", "shared-web", kind}
	case "react-native":
		return []string{"shared", kind}
	default:
		return []string{kind}
	}
}

// ListFrontendTree returns every file a frontend of the given kind is
// scaffolded from, sorted by destination path. Shadowed files appear once,
// resolved to the winning root.
func ListFrontendTree(kind string) ([]FrontendTreeFile, error) {
	winner := make(map[string]string)
	for _, root := range FrontendTemplateRoots(kind) {
		files, err := listTemplates(path.Join("frontend", root), true)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			winner[f] = path.Join(root, f)
		}
	}

	out := make([]FrontendTreeFile, 0, len(winner))
	for rel, full := range winner {
		out = append(out, FrontendTreeFile{Rel: rel, Path: full})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}
