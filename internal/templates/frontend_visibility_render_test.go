package templates

import (
	"strings"
	"testing"
)

// TestEveryEnvDeclaresTheFrontendCapability pins the UX property that the
// deploy templates exist to carry, and it is a UX property rather than a
// correctness one — which is exactly why it needs a test.
//
// A frontend binds a runtime in every env (ADR 0002 §6). That capability was
// once declared in the DEV template only: staging and prod emitted no
// frontend at all, so the only way to discover forge could ship one was to
// read kcl/schema.k, and a user who never did shipped a backend to prod and
// served their frontend by hand. A capability nobody can find is one that
// does not exist. Now every env declares the frontend AND binds it, because a
// frontend with no runtime is a render error.
func TestEveryEnvDeclaresTheFrontendCapability(t *testing.T) {
	t.Parallel()

	data := EnvTemplateData{ProjectName: "acme", EnvName: "prod", IngressEnabled: true, HasFrontend: true, PrimaryWorkload: "acme", FrontendName: "web"}

	for tmpl, runtime := range map[string]string{
		"kcl/dev/main.k.tmpl":   "runtime = forge.OnHost {}",
		"kcl/cloud/main.k.tmpl": "_hosted_frontend(_web_frontend)",
	} {
		t.Run(tmpl, func(t *testing.T) {
			t.Parallel()

			out, err := DeployTemplates().Render(tmpl, data)
			if err != nil {
				t.Fatalf("rendering %s: %v", tmpl, err)
			}
			rendered := string(out)
			if !strings.Contains(rendered, "forge.Frontend {") || !strings.Contains(rendered, `name = "web"`) {
				t.Fatalf("%s declares NO frontend for a project that has one", tmpl)
			}
			if !strings.Contains(rendered, runtime) {
				t.Fatalf("%s does not bind the frontend's runtime (%s): a frontend with no runtime is a render error", tmpl, runtime)
			}
		})
	}
}

// TestFrontendBlockIsAbsentWithoutAFrontend is the counterweight. Without it
// the test above is satisfiable by emitting the block unconditionally, which
// would put a forge.Frontend naming an empty path into every backend-only
// project — a render error at best and a confusing one at worst.
func TestFrontendBlockIsAbsentWithoutAFrontend(t *testing.T) {
	t.Parallel()

	data := EnvTemplateData{ProjectName: "acme", EnvName: "prod", IngressEnabled: true, HasFrontend: false, PrimaryWorkload: "acme"}

	for _, tmpl := range []string{
		"kcl/dev/main.k.tmpl",
		"kcl/cloud/main.k.tmpl",
	} {
		out, err := DeployTemplates().Render(tmpl, data)
		if err != nil {
			t.Fatalf("rendering %s: %v", tmpl, err)
		}
		// The cloud env declares its frontend BINDERS either way (a frontend
		// added later binds through them); what must be absent is a
		// declared frontend and a frontends list.
		for _, declared := range []string{"= forge.Frontend {", "frontends = ["} {
			if strings.Contains(string(out), declared) {
				t.Fatalf("%s emitted a frontend (%q) for a project with no frontend", tmpl, declared)
			}
		}
	}
}

// TestCloudFrontendBucketIsNeverGuessed pins a decision that is easy to
// "improve" into a bug.
//
// forge does not guess where a frontend is published to a bucket. Bucket
// names are GLOBAL: a derived name ("acme-prod-web") would publish into
// whichever bucket of that name exists, possibly someone else's, and a real
// one costs money the day it is created. The cloud env publishes the site to
// the platform's static hosting, which owns its own bucket; the own-bucket
// binder is declared beside it with NO bucket (`_bucket = None`) and an
// example in a comment, and binding a frontend to it fails the render until
// the author declares one. Nothing is a REPLACE_ME, because nothing is bound
// to it as scaffolded.
func TestCloudFrontendBucketIsNeverGuessed(t *testing.T) {
	t.Parallel()

	data := EnvTemplateData{ProjectName: "acme", EnvName: "prod", IngressEnabled: true, HasFrontend: true, PrimaryWorkload: "acme", FrontendName: "web"}
	out, err := DeployTemplates().Render("kcl/cloud/main.k.tmpl", data)
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(out)
	if !strings.Contains(rendered, "\n_bucket = None\n") {
		t.Fatalf("the cloud env does not leave the own-bucket binder without a bucket:\n%s", rendered)
	}
	for _, line := range strings.Split(rendered, "\n") {
		if code, _, _ := strings.Cut(line, "#"); strings.Contains(code, "bucket = \"") {
			t.Fatalf("the cloud env names a bucket outside a comment: %q", line)
		}
	}
	if strings.Contains(rendered, "REPLACE_ME") {
		t.Fatalf("the cloud env is born with a placeholder:\n%s", rendered)
	}
}
