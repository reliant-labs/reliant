package doctor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakePprof(t *testing.T, cmdline string) *Environment {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/debug/pprof/":
			_, _ = w.Write([]byte(`<tr><td>12</td><td><a href='goroutine?debug=1'>goroutine</a></td></tr>`))
		case "/debug/pprof/cmdline":
			_, _ = w.Write([]byte(cmdline))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	env := &Environment{ProjectName: "shop"}
	env.SetPort("app", 6060, strings.TrimPrefix(srv.URL, "http://"))
	return env
}

// The app lost the shared 6060 bind to another process, and the check
// reported that process's profiles as the app's. The responder must be the
// app before the check passes.
func TestCheckPprof_AnotherProcessesListenerIsNotAPass(t *testing.T) {
	got := CheckPprof(context.Background(), fakePprof(t, "/usr/local/bin/reliant\x00daemon"))
	if got.Status == StatusPass {
		t.Fatalf("pprof passed on another process's listener: %s", got.Message)
	}
	if !strings.Contains(got.Message, "/usr/local/bin/reliant") {
		t.Fatalf("the warning should name the process that answered: %s", got.Message)
	}
}

func TestCheckPprof_TheAppsOwnListenerPasses(t *testing.T) {
	got := CheckPprof(context.Background(), fakePprof(t, "/home/dev/shop/tmp/shop\x00server"))
	if got.Status != StatusPass {
		t.Fatalf("want pass, got %s: %s", got.Status, got.Message)
	}
}
