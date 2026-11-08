package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// controlPlaneServer answers the procedures in replies (keyed by the short
// procedure name) and fails the test on any other. A reply of "" is a
// connect not_found.
func controlPlaneServer(t *testing.T, replies map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		short := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		reply, ok := replies[short]
		if !ok {
			t.Errorf("unexpected call %s", r.URL.Path)
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if reply == "" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"not_found","message":"environment not found"}`)
			return
		}
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The preview names exactly what goes: the env's id, project, control plane,
// deployment count and the custom domains bound to it — those because the
// control plane refuses the delete while they stay bound (#1547).
func TestRunEnvDelete_PreviewShowsWhatWillBeDeleted(t *testing.T) {
	f := &fakeLifecycleCaller{replies: map[string]string{
		"ListDeployments": twoDeployments,
		"ListDomains": `{"domains":[
		  {"id":"d1","hostname":"www.shop.example","binding":{"environmentId":"env_1","target":"web"}},
		  {"id":"d2","hostname":"shop.example","binding":{"environmentId":"env_1","target":"web"}},
		  {"id":"d3","hostname":"other.example","binding":{"environmentId":"env_2","target":"web"}},
		  {"id":"d4","hostname":"unbound.example"}]}`,
	}}
	var out bytes.Buffer
	opts := envDeleteOptions{Yes: true, Project: "shop", Endpoint: "https://cp.example"}
	if err := runEnvDelete(context.Background(), f, "env_1", "prod-web", opts, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"id:             env_1", "project:        shop", "control plane:  https://cp.example", "deployments:    2",
		"custom domains: shop.example, www.shop.example", "forge domain unbind shop.example --env",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("preview missing %q:\n%s", want, out.String())
		}
	}
	for _, not := range []string{"other.example", "unbound.example"} {
		if strings.Contains(out.String(), not) {
			t.Errorf("preview lists %s, which is not bound to this env:\n%s", not, out.String())
		}
	}
}

// A domain list that cannot be read leaves the preview incomplete, so the
// delete stops before confirming and never calls DeleteEnvironment.
func TestRunEnvDelete_UnreadableDomainListStopsTheDelete(t *testing.T) {
	f := &fakeLifecycleCaller{errs: map[string]error{"ListDomains": &cloud.Error{Code: cloud.CodeUnavailable, Message: "domain service down"}}}
	var out bytes.Buffer
	err := runEnvDelete(context.Background(), f, "env_1", "prod-web", envDeleteOptions{Yes: true}, strings.NewReader(""), &out)
	if err == nil || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Fatalf("err = %v; want the delete refused for an incomplete preview", err)
	}
	if !strings.Contains(out.String(), "custom domains: could not be read") {
		t.Errorf("preview should say the domains could not be read:\n%s", out.String())
	}
	for _, c := range f.calls {
		if c.proc == procDeleteEnvironment {
			t.Fatal("an unreadable domain list must stop the delete before DeleteEnvironment")
		}
	}
}

// A control plane with no domain service has no domain to bind: its preview
// is complete, and the delete proceeds.
func TestRunEnvDelete_NoDomainServiceMeansNoDomains(t *testing.T) {
	f := &fakeLifecycleCaller{errs: map[string]error{"ListDomains": &cloud.Error{Code: cloud.CodeUnimplemented, Message: "no domain service"}}}
	var out bytes.Buffer
	if err := runEnvDelete(context.Background(), f, "env_1", "prod-web", envDeleteOptions{Yes: true}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "custom domains: none (this control plane has no domain service)") {
		t.Errorf("preview should say the control plane has no domain service:\n%s", out.String())
	}
	if last := f.calls[len(f.calls)-1]; last.proc != procDeleteEnvironment {
		t.Errorf("no domain service must not stop the delete; last call %s", last.proc)
	}
}

// The control plane's refusal of an env with a bound domain reaches the user
// as the control plane said it.
func TestRunEnvDelete_DomainBoundRefusalIsTheControlPlanes(t *testing.T) {
	refusal := &cloud.Error{Code: cloud.CodeFailedPrecondition, Message: "custom domain shop.example is bound to this environment"}
	f := &fakeLifecycleCaller{errs: map[string]error{"DeleteEnvironment": refusal}}
	err := runEnvDelete(context.Background(), f, "env_1", "prod-web", envDeleteOptions{Yes: true}, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !errors.Is(err, refusal) || !strings.Contains(err.Error(), "shop.example is bound") {
		t.Fatalf("err = %v; want the control plane's refusal", err)
	}
}

// `forge env delete --id` reads the env by the control plane's id, on a
// control plane this project has used, and refuses an env that is not this
// project's, not the name given, or still declared.
func TestEnvDelete_ResolvesByID(t *testing.T) {
	srv := controlPlaneServer(t, map[string]string{
		"GetEnvironment": `{"environment":{"id":"env_9","name":"prod-web","project":"shop"}}`,
	})

	t.Run("an undeclared env", func(t *testing.T) {
		undeclaredDeleteProject(t, srv.URL)
		got, err := resolveLifecycleTargetFromID(t.Context(), "env_9", "", "tok", "")
		if err != nil || got.EnvironmentID != "env_9" || got.Name != "prod-web" || got.Project != "shop" || got.Endpoint != srv.URL {
			t.Fatalf("target = %+v, %v", got, err)
		}
	})
	t.Run("with its name", func(t *testing.T) {
		undeclaredDeleteProject(t, srv.URL)
		if _, err := resolveLifecycleTargetFromID(t.Context(), "env_9", "prod-web", "tok", ""); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("with another name", func(t *testing.T) {
		undeclaredDeleteProject(t, srv.URL)
		_, err := resolveLifecycleTargetFromID(t.Context(), "env_9", "staging", "tok", "")
		if err == nil || !strings.Contains(err.Error(), `named "prod-web"`) {
			t.Fatalf("err = %v; want the name mismatch refused", err)
		}
	})
	t.Run("another project's env", func(t *testing.T) {
		undeclaredDeleteProject(t, srv.URL)
		withProjectName(t, "blog")
		_, err := resolveLifecycleTargetFromID(t.Context(), "env_9", "", "tok", "")
		if err == nil || !strings.Contains(err.Error(), `belongs to project "shop"`) {
			t.Fatalf("err = %v; want another project's env refused", err)
		}
	})
	t.Run("a declared env", func(t *testing.T) {
		declared := controlPlaneServer(t, map[string]string{
			"GetEnvironment": `{"environment":{"id":"env_1","name":"prod","project":"shop"}}`,
		})
		undeclaredDeleteProject(t, declared.URL)
		_, err := resolveLifecycleTargetFromID(t.Context(), "env_1", "", "tok", "")
		if err == nil || !strings.Contains(err.Error(), "`forge env delete prod`") {
			t.Fatalf("err = %v; want a declared env sent to delete-by-name", err)
		}
	})
	t.Run("a reply that omits the env's name", func(t *testing.T) {
		nameless := controlPlaneServer(t, map[string]string{
			"GetEnvironment": `{"environment":{"id":"env_7","project":"shop"}}`,
		})
		undeclaredDeleteProject(t, nameless.URL)
		_, err := resolveLifecycleTargetFromID(t.Context(), "env_7", "", "tok", "")
		if err == nil || !strings.Contains(err.Error(), "no usable name") {
			t.Fatalf("err = %v; want a nameless env refused before the declaration check", err)
		}
	})
	t.Run("a reply that omits the env's project", func(t *testing.T) {
		projectless := controlPlaneServer(t, map[string]string{
			"GetEnvironment": `{"environment":{"id":"env_8","name":"prod-web"}}`,
		})
		undeclaredDeleteProject(t, projectless.URL)
		_, err := resolveLifecycleTargetFromID(t.Context(), "env_8", "", "tok", "")
		if err == nil || !strings.Contains(err.Error(), "with no project") {
			t.Fatalf("err = %v; want an env of no named project refused", err)
		}
	})
	t.Run("an id the control plane does not hold", func(t *testing.T) {
		missing := controlPlaneServer(t, map[string]string{"GetEnvironment": ""})
		undeclaredDeleteProject(t, missing.URL)
		_, err := resolveLifecycleTargetFromID(t.Context(), "env_gone", "", "tok", "")
		if err == nil || !strings.Contains(err.Error(), "nothing to delete") {
			t.Fatalf("err = %v; want nothing to delete", err)
		}
	})
	t.Run("no control plane to ask", func(t *testing.T) {
		undeclaredDeleteProject(t)
		_, err := resolveLifecycleTargetFromID(t.Context(), "env_9", "", "tok", "")
		if err == nil || !strings.Contains(err.Error(), "forge env delete --id env_9 --endpoint "+cloud.DefaultEndpoint) {
			t.Fatalf("err = %v; want an --endpoint fix", err)
		}
	})
}

func TestEnvDeleteCmd_ByID(t *testing.T) {
	oldByID := resolveLifecycleTargetByID
	t.Cleanup(func() { resolveLifecycleTargetByID = oldByID })
	f := &fakeLifecycleCaller{replies: map[string]string{"ListDeployments": twoDeployments}}
	var gotID, gotName string
	resolveLifecycleTargetByID = func(_ context.Context, id, env, _, _ string) (lifecycleTarget, error) {
		gotID, gotName = id, env
		return lifecycleTarget{Client: f, EnvironmentID: id, Endpoint: "https://cp.example", Name: "prod-web", Project: "shop"}, nil
	}

	cmd := newEnvDeleteCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--id", "env_9", "--yes"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotID != "env_9" || gotName != "" {
		t.Errorf("resolved id=%q name=%q", gotID, gotName)
	}
	last := f.calls[len(f.calls)-1]
	if last.proc != procDeleteEnvironment || last.req["environmentId"] != "env_9" {
		t.Errorf("last call = %+v", last)
	}
	if !strings.Contains(out.String(), `hosted env "prod-web"`) || !strings.Contains(out.String(), "project:        shop") {
		t.Errorf("the preview should name the env the id resolved to:\n%s", out.String())
	}

	cmd = newEnvDeleteCmd()
	cmd.SetArgs([]string{"--yes"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--id <environment-id>") {
		t.Fatalf("err = %v; want a name or --id asked for", err)
	}
}

// `forge env list --orphaned` lists this project's hosted envs that
// deploy/kcl/ no longer declares — and only this project's.
func TestEnvListOrphaned(t *testing.T) {
	srv := controlPlaneServer(t, map[string]string{
		"ListEnvironments": `{"environments":[
		  {"id":"env_1","name":"prod","project":"shop"},
		  {"id":"env_9","name":"prod-web","project":"shop"},
		  {"id":"env_7","name":"legacy","project":"blog"}]}`,
	})

	t.Run("lists the undeclared env", func(t *testing.T) {
		undeclaredDeleteProject(t, srv.URL)
		var out bytes.Buffer
		if err := runEnvListOrphaned(t.Context(), projectDirForKCL(), "tok", "", &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "prod-web") || !strings.Contains(out.String(), "env_9") || !strings.Contains(out.String(), "forge env delete <env>") {
			t.Errorf("want prod-web listed with its id and the delete hint:\n%s", out.String())
		}
		for _, not := range []string{"env_1", "legacy"} {
			if strings.Contains(out.String(), not) {
				t.Errorf("lists %s, which is declared or another project's:\n%s", not, out.String())
			}
		}
	})
	t.Run("nothing orphaned", func(t *testing.T) {
		clean := controlPlaneServer(t, map[string]string{
			"ListEnvironments": `{"environments":[{"id":"env_1","name":"prod","project":"shop"}]}`,
		})
		undeclaredDeleteProject(t, clean.URL)
		var out bytes.Buffer
		if err := runEnvListOrphaned(t.Context(), projectDirForKCL(), "tok", "", &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "no orphaned hosted envs") {
			t.Errorf("out = %s", out.String())
		}
	})
	t.Run("no control plane to ask", func(t *testing.T) {
		undeclaredDeleteProject(t)
		err := runEnvListOrphaned(t.Context(), projectDirForKCL(), "tok", "", io.Discard)
		if err == nil || !strings.Contains(err.Error(), "--endpoint "+cloud.DefaultEndpoint) {
			t.Fatalf("err = %v; want an --endpoint fix", err)
		}
	})
	t.Run("an --endpoint the project never used", func(t *testing.T) {
		undeclaredDeleteProject(t, srv.URL)
		err := runEnvListOrphaned(t.Context(), projectDirForKCL(), "tok", "https://elsewhere.example", io.Discard)
		if err == nil || !strings.Contains(err.Error(), "not a control plane this project has used") {
			t.Fatalf("err = %v; want it refused before the credential is sent", err)
		}
	})
}
