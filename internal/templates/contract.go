// Package templates owns the embedded forge template tree and renders it
// into scaffolded code.
//
// The behavioural seam exposed by the package is the [Service] interface:
// callers (codegen, generator, packs, docs) hold a Service handle and ask
// it to render a template by category + name. The data carriers
// (TemplateCategory, TemplateEngine, *TemplateData structs) are utility
// types kept on the package surface for backward compatibility with
// scaffolders that walk individual category trees.
package templates

// Service is the behavioural surface of the templates package.
//
// Operations are intentionally narrow: callers pick a category by name
// and ask for either a list of templates or a rendered template body.
// More specialised entry points (TemplateEngine, RenderFromFS) remain as
// package-level helpers because their inputs (pre-parsed template, custom
// fs.FS) make a uniform interface awkward.
type Service interface {
	// RenderCategory renders the named template within the given category
	// (e.g. category="project", name="bootstrap.go.tmpl") with data and
	// returns the post-processed bytes.
	RenderCategory(category, name string, data any) ([]byte, error)

	// ListCategory returns all template paths under the named category,
	// optionally restricted to a sub-directory. Recursive walk.
	ListCategory(category, subdir string) ([]string, error)

	// RenderInternalPackageKind renders a template from an internal-package
	// kind sub-directory (e.g. kind="client", name="client.go.tmpl").
	RenderInternalPackageKind(kind, name string, data any) ([]byte, error)
}

// Deps is the dependency set for the templates Service. Empty today; the
// package owns its embedded FS and has no external collaborators.
type Deps struct{}

// New constructs a templates.Service.
//
// forge:no-observe
// Pure compute: empty Deps; the package reads its own embedded FS. There
// is no I/O boundary worth instrumenting.
func New(_ Deps) Service { return &svc{} }

type svc struct{}

// RenderCategory dispatches to the matching package-level category accessor.
func (s *svc) RenderCategory(category, name string, data any) ([]byte, error) {
	return categoryFor(category).Render(name, data)
}

// ListCategory walks the embedded FS under the named category.
func (s *svc) ListCategory(category, subdir string) ([]string, error) {
	return categoryFor(category).List(subdir)
}

// RenderInternalPackageKind renders a kind-scoped internal-package template.
func (s *svc) RenderInternalPackageKind(kind, name string, data any) ([]byte, error) {
	return RenderInternalPackageKindTemplate(kind, name, data)
}

// categoryFor maps a user-facing category name to its TemplateCategory.
// Unknown names produce an empty category whose subsequent Render/List
// calls surface a clear "file not found" error from the embedded FS.
func categoryFor(name string) TemplateCategory {
	switch name {
	case "project":
		return ProjectTemplates()
	case "frontend":
		return FrontendTemplates()
	case "deploy":
		return DeployTemplates()
	case "test":
		return TestTemplates()
	case "internal-package":
		return InternalPkgTemplates()
	case "service":
		return ServiceTemplates()
	case "webhook":
		return WebhookTemplates()
	case "worker":
		return WorkerTemplates()
	case "worker-cron":
		return WorkerCronTemplates()
	case "operator":
		return OperatorTemplates()
	default:
		return TemplateCategory{basePath: name}
	}
}
