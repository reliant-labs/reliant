package httpaction

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
)

// A fan-out action lists parents, then runs one request for each. These tests
// use a made-up API (orgs, then each org's projects) so they pin the runner's
// behaviour, not GitHub's.

const fanOutAction = `    mutates: false
    params:
      type: object
      properties:
        org: { type: string }
    request:
      method: GET
      path: /orgs
      pagination: { style: link_header, max_pages: 5 }
      fan_out:
        items: "has(params.org) ? response.orgs.filter(o, o.name == params.org) : response.orgs"
        max_items: 3
        skip_status: [404]
        request:
          method: GET
          path: "/orgs/{{ string(int(parent.id)) }}/projects"
          pagination: { style: link_header, max_pages: 5 }
    output:
      select: "response.projects.map(p, {'name': p.name, 'org': parent.name})"
      schema: { type: object }
`

type fanOutServer struct {
	*httptest.Server
	mu   sync.Mutex
	hits []string
}

func (f *fanOutServer) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hits...)
}

func newFanOutServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, srvURL string) bool) *fanOutServer {
	t.Helper()
	f := &fanOutServer{}
	f.Server = netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits = append(f.hits, r.URL.RequestURI())
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if handler(w, r, f.URL) {
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	return f
}

func rootCAs(f *fanOutServer) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(f.Certificate())
	return pool
}

func runFanOut(t *testing.T, f *fanOutServer, params map[string]any) (*Result, error) {
	t.Helper()
	m, a := mustParse(t, manifestFor(f.URL, fanOutAction))
	return newRunner().WithRootCAs(rootCAs(f)).Run(context.Background(), m, a, params)
}

func TestFanOutWalksParentsAndTheirPages(t *testing.T) {
	f := newFanOutServer(t, func(w http.ResponseWriter, r *http.Request, base string) bool {
		switch r.URL.Path {
		case "/orgs":
			// Two pages of parents.
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `{"orgs":[{"id":2,"name":"beta"}]}`)
				return true
			}
			w.Header().Set("Link", `<`+base+`/orgs?page=2>; rel="next"`)
			fmt.Fprint(w, `{"orgs":[{"id":1,"name":"alpha"}]}`)
			return true
		case "/orgs/1/projects":
			// Two pages of children for the first parent.
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `{"projects":[{"name":"a2"}]}`)
				return true
			}
			w.Header().Set("Link", `<`+base+`/orgs/1/projects?page=2>; rel="next"`)
			fmt.Fprint(w, `{"projects":[{"name":"a1"}]}`)
			return true
		case "/orgs/2/projects":
			fmt.Fprint(w, `{"projects":[{"name":"b1"}]}`)
			return true
		}
		return false
	})
	res, err := runFanOut(t, f, nil)
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.Equal(t, []any{
		map[string]any{"name": "a1", "org": "alpha"},
		map[string]any{"name": "a2", "org": "alpha"},
		map[string]any{"name": "b1", "org": "beta"},
	}, res.Data["items"], "every parent's pages, in order, each shaped with its parent")
	assert.Len(t, f.paths(), 5, "two parent pages, then two pages and one page of children")
}

func TestFanOutItemsExpressionNarrowsTheParents(t *testing.T) {
	f := newFanOutServer(t, func(w http.ResponseWriter, r *http.Request, _ string) bool {
		switch r.URL.Path {
		case "/orgs":
			fmt.Fprint(w, `{"orgs":[{"id":1,"name":"alpha"},{"id":2,"name":"beta"}]}`)
			return true
		case "/orgs/2/projects":
			fmt.Fprint(w, `{"projects":[{"name":"b1"}]}`)
			return true
		}
		return false
	})
	res, err := runFanOut(t, f, map[string]any{"org": "beta"})
	require.NoError(t, err)
	assert.Equal(t, []any{map[string]any{"name": "b1", "org": "beta"}}, res.Data["items"])
	for _, p := range f.paths() {
		assert.NotContains(t, p, "/orgs/1/", "a parent filtered out is not walked")
	}
}

func TestFanOutDropsAParentThatWentAway(t *testing.T) {
	f := newFanOutServer(t, func(w http.ResponseWriter, r *http.Request, _ string) bool {
		switch r.URL.Path {
		case "/orgs":
			fmt.Fprint(w, `{"orgs":[{"id":1,"name":"alpha"},{"id":2,"name":"beta"}]}`)
			return true
		case "/orgs/2/projects":
			fmt.Fprint(w, `{"projects":[{"name":"b1"}]}`)
			return true
		}
		return false // /orgs/1/projects is a 404: listed, then gone
	})
	res, err := runFanOut(t, f, nil)
	require.NoError(t, err)
	assert.Equal(t, []any{map[string]any{"name": "b1", "org": "beta"}}, res.Data["items"], "skip_status lets the walk go on without it")
}

func TestFanOutFailsOnAFailureItMayNotSkip(t *testing.T) {
	for name, tc := range map[string]struct {
		status    int
		retryable bool
	}{
		"a server error":    {http.StatusInternalServerError, true},
		"a permission wall": {http.StatusForbidden, false}, // 403 is not in skip_status here
	} {
		t.Run(name, func(t *testing.T) {
			f := newFanOutServer(t, func(w http.ResponseWriter, r *http.Request, _ string) bool {
				if r.URL.Path == "/orgs" {
					fmt.Fprint(w, `{"orgs":[{"id":1,"name":"alpha"},{"id":2,"name":"beta"}]}`)
					return true
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"message":"no"}`)
				return true
			})
			res, err := runFanOut(t, f, nil)
			require.NoError(t, err)
			assert.True(t, res.IsError, "a walk that lost a parent for a reason other than it going away must not read as complete")
			assert.Equal(t, tc.status, res.StatusCode)
			assert.Equal(t, tc.retryable, res.Retryable)
			assert.Len(t, f.paths(), 2, "stopped at the first failing parent")
		})
	}
}

func TestFanOutFailsWhenTheParentRequestFails(t *testing.T) {
	f := newFanOutServer(t, func(w http.ResponseWriter, r *http.Request, _ string) bool {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"Bad credentials"}`)
		return true
	})
	res, err := runFanOut(t, f, nil)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Equal(t, http.StatusUnauthorized, res.StatusCode)
	assert.Len(t, f.paths(), 1)
}

func TestFanOutRefusesMoreParentsThanItMayWalk(t *testing.T) {
	f := newFanOutServer(t, func(w http.ResponseWriter, r *http.Request, _ string) bool {
		if r.URL.Path == "/orgs" {
			fmt.Fprint(w, `{"orgs":[{"id":1,"name":"a"},{"id":2,"name":"b"},{"id":3,"name":"c"},{"id":4,"name":"d"}]}`)
			return true
		}
		return false
	})
	_, err := runFanOut(t, f, nil)
	require.Error(t, err, "max_items is 3: quietly walking some of them would read as complete")
	assert.Contains(t, err.Error(), "at most 3")
	assert.Len(t, f.paths(), 1, "no child was requested")
}

func TestFanOutOfNothingIsAnEmptyList(t *testing.T) {
	f := newFanOutServer(t, func(w http.ResponseWriter, r *http.Request, _ string) bool {
		fmt.Fprint(w, `{"orgs":[]}`)
		return true
	})
	res, err := runFanOut(t, f, nil)
	require.NoError(t, err)
	assert.Equal(t, []any{}, res.Data["items"], "an empty list, not null: a workflow can take its size")
}

func TestFanOutIsValidatedAtLoad(t *testing.T) {
	base := "https://api.example.com"
	cases := map[string]struct{ old, new, want string }{
		"parent request does not paginate": {"      pagination: { style: link_header, max_pages: 5 }\n      fan_out:", "      fan_out:", "must paginate"},
		"child request does not paginate":  {"          path: \"/orgs/{{ string(int(parent.id)) }}/projects\"\n          pagination: { style: link_header, max_pages: 5 }\n", "          path: \"/orgs/{{ string(int(parent.id)) }}/projects\"\n", "request must paginate"},
		"no items expression":              {"        items: \"has(params.org) ? response.orgs.filter(o, o.name == params.org) : response.orgs\"\n", "", "items is required"},
		"items does not compile":           {"response.orgs.filter(o, o.name == params.org) : response.orgs", "response.orgs.filter( : response.orgs", "items"},
		"too many parents allowed":         {"max_items: 3", "max_items: 500", "max_items"},
		"skip a success":                   {"skip_status: [404]", "skip_status: [200]", "error status"},
		"child leaves the host":            {"path: \"/orgs/{{ string(int(parent.id)) }}/projects\"", "url: \"https://evil.example.net/x\"", "outside base_url"},
		"child path does not compile":      {"{{ string(int(parent.id)) }}", "{{ string( }}", "request"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			doc := manifestFor(base, fanOutAction)
			require.Contains(t, doc, tc.old)
			_, err := manifest.Parse([]byte(strings.Replace(doc, tc.old, tc.new, 1)), manifest.TrustCurated)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	t.Run("a walk may not nest", func(t *testing.T) {
		doc := strings.Replace(manifestFor(base, fanOutAction),
			"          pagination: { style: link_header, max_pages: 5 }\n    output:",
			"          pagination: { style: link_header, max_pages: 5 }\n          fan_out: { items: \"response.projects\", request: { method: GET, path: /x, pagination: { style: link_header } } }\n    output:", 1)
		_, err := manifest.Parse([]byte(doc), manifest.TrustCurated)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "one level deep")
	})
}
