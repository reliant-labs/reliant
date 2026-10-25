package grpc

// The web app calls this server CROSS-ORIGIN in prod (app.reliantlabs.io ->
// api.reliantapi.com), so its CORS policy is a contract with the web client's
// source, in both directions:
//
//   - every request header the client attaches must be in the preflight
//     allow-list, or the browser refuses to send the RPC at all;
//   - every response header the client reads must be exposed, or the browser
//     hides it and the code keyed on it silently never runs.
//
// Neither failure is visible in dev, where the Vite proxy makes RPCs
// same-origin, nor to Go tests that call handlers directly. That is how
// x-daemon-last-seen — attached to EVERY request once a cloud machine
// heartbeats — took the web app down in prod months after it shipped.
//
// So these tests read the client's TypeScript source instead of a list copied
// from it; a copied list is exactly what drifted. Every file is read with
// os.ReadFile / os.ReadDir in this process, so Go's test cache sees the inputs
// and an edit under frontends/web/src re-runs these tests even under -short.

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const webClientTestOrigin = "https://app.reliantlabs.io"

// connectWebProtocolHeaders are set by @connectrpc/connect-web itself
// (protocol-connect/request-header.js: Content-Type and
// Connect-Protocol-Version always, Connect-Timeout-Ms when a call carries a
// timeout), not by our interceptors, so the source scan cannot see them.
var connectWebProtocolHeaders = []string{"Content-Type", "Connect-Protocol-Version", "Connect-Timeout-Ms"}

// traceHeaders are written onto every RPC by the forge web runtime's
// traceInterceptor and the HyperDX SDK's fetch instrumentation — both inject
// whatever the global OTel propagator writes (injectTraceContext), which is
// W3C trace-context plus W3C baggage while nothing installs its own. They live
// in node_modules, not in frontends/web/src, so the source scan cannot see them.
// requireDefaultPropagator keeps that assumption honest.
var traceHeaders = []string{"traceparent", "tracestate", "baggage"}

// controlPlaneOnlyRequestHeaders are sent only on control-plane transports
// (TransportBackend "control-plane" in api/transport.ts), whose CORS allow-list
// is forge's default, not corsAllowedHeaders. This server never receives them,
// so allowing them here would widen the policy for nothing.
var controlPlaneOnlyRequestHeaders = []string{"Forge-Via"}

var (
	// req.header.set("x-foo", …) / header.append(NAME, …) — the Connect
	// interceptor API. Captures the first argument.
	requestHeaderSetRe = regexp.MustCompile(`\bheader\.(?:set|append)\(\s*([^,)]+?)\s*,`)
	// error.metadata.get(…), res.trailer.get(…), responseHeader.get(…) — how a
	// Connect client reads response headers and trailers.
	responseHeaderGetRe = regexp.MustCompile(`\b(?:metadata|trailer|responseHeader)\.get\(\s*([^)]+?)\s*\)`)
	stringLiteralRe     = regexp.MustCompile("^(?:\"([^\"]*)\"|'([^']*)'|`([^`$]*)`)$")
	identifierRe        = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
)

func TestCORSAllowsEveryWebClientRequestHeader(t *testing.T) {
	webSrc := webSourceDir(t)
	requireDefaultPropagator(t, webSrc)

	sent := scanWebHeaderNames(t, webSrc, requestHeaderSetRe, nil)
	// Guard the scanner itself: a regex that silently matches nothing would
	// make this test pass vacuously.
	if _, ok := sent["authorization"]; !ok {
		t.Fatalf("scanner found no Authorization header in frontends/web/src (found %v); the extraction regex is broken", sortedKeys(sent))
	}
	for _, h := range controlPlaneOnlyRequestHeaders {
		delete(sent, strings.ToLower(h))
	}
	for _, h := range connectWebProtocolHeaders {
		sent[strings.ToLower(h)] = append(sent[strings.ToLower(h)], "@connectrpc/connect-web")
	}
	for _, h := range traceHeaders {
		sent[h] = append(sent[h], "@reliantlabs/forge-web-runtime traceInterceptor")
	}

	handler := newCORSHandler([]string{webClientTestOrigin})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, name := range sortedKeys(sent) {
		req := httptest.NewRequest(http.MethodOptions, "/reliant.v1.FileSystemService/GetFileTree", nil)
		req.Header.Set("Origin", webClientTestOrigin)
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		req.Header.Set("Access-Control-Request-Headers", name)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if !headerListContains(rec.Header().Get("Access-Control-Allow-Headers"), name) {
			t.Errorf("the web client sends %q (%s), but the CORS preflight does not allow it: "+
				"browsers will block every cross-origin RPC that carries it. Add it to corsAllowedHeaders in server.go",
				name, strings.Join(sent[name], ", "))
		}
	}
}

func TestCORSExposesEveryHeaderTheWebClientReads(t *testing.T) {
	webSrc := webSourceDir(t)

	read := scanWebHeaderNames(t, webSrc, responseHeaderGetRe, nil)
	if _, ok := read["x-forge-error-reason"]; !ok {
		t.Fatalf("scanner found no x-forge-error-reason read in frontends/web/src (found %v); the extraction regex is broken", sortedKeys(read))
	}

	handler := newCORSHandler([]string{webClientTestOrigin})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodPost, "/reliant.v1.FileSystemService/GetFileTree", nil)
	req.Header.Set("Origin", webClientTestOrigin)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	exposed := rec.Header().Get("Access-Control-Expose-Headers")

	for _, name := range sortedKeys(read) {
		if !headerListContains(exposed, name) {
			t.Errorf("the web client reads response header %q (%s), but CORS does not expose it: "+
				"cross-origin, the browser hides it and the client reads it as absent. Add it to corsExposedHeaders in server.go",
				name, strings.Join(read[name], ", "))
		}
	}
}

// webSourceDir is the web client's source root, two levels above this
// package's source directory (the working directory `go test` runs it in).
//
// Not runtime.Caller: under -trimpath (the Reliant workspace image's GOFLAGS
// default) it reports a module-relative path, not a file on disk.
func webSourceDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "frontends", "web", "src"))
	if err != nil {
		t.Fatalf("resolve frontends/web/src: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("web client source not found at %s: %v", dir, err)
	}
	return dir
}

// scanWebHeaderNames returns every header name the pattern's first capture
// group names in the web client's hand-written source, lowercased, mapped to
// the places that name it. An argument that is neither a string literal nor a
// string const declared in the same file must be listed in dynamic, or the
// test fails: an unresolvable name is a header this contract cannot check.
func scanWebHeaderNames(t *testing.T, webSrc string, pattern *regexp.Regexp, dynamic map[string][]string) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	err := filepath.WalkDir(webSrc, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "gen", "__tests__", "__mocks__", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !isHandWrittenTS(d.Name()) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(webSrc, path)
		rel = filepath.ToSlash(rel)
		text := string(src)
		for _, m := range pattern.FindAllStringSubmatchIndex(text, -1) {
			expr := text[m[2]:m[3]]
			where := rel + ":" + lineOf(text, m[0])
			names, ok := resolveHeaderName(text, expr)
			if !ok {
				names, ok = dynamic[rel+":"+expr]
			}
			if !ok {
				t.Errorf("%s: cannot resolve header name %q. Use a string literal or a same-file string const, "+
					"or list the expression in dynamicRequestHeaders with the headers it can produce", where, expr)
				continue
			}
			for _, n := range names {
				found[strings.ToLower(n)] = append(found[strings.ToLower(n)], where)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scanning %s: %v", webSrc, err)
	}
	return found
}

func isHandWrittenTS(name string) bool {
	if !strings.HasSuffix(name, ".ts") && !strings.HasSuffix(name, ".tsx") {
		return false
	}
	for _, suffix := range []string{".d.ts", ".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx"} {
		if strings.HasSuffix(name, suffix) {
			return false
		}
	}
	return true
}

// resolveHeaderName reads expr as a string literal, or as an identifier bound
// to a string literal by a const/let/var declaration in the same file.
func resolveHeaderName(fileText, expr string) ([]string, bool) {
	if m := stringLiteralRe.FindStringSubmatch(expr); m != nil {
		return []string{m[1] + m[2] + m[3]}, true
	}
	if !identifierRe.MatchString(expr) {
		return nil, false
	}
	decl := regexp.MustCompile(`\b(?:const|let|var)\s+` + regexp.QuoteMeta(expr) +
		`\s*(?::\s*string\s*)?=\s*(?:"([^"]*)"|'([^']*)')`)
	if m := decl.FindStringSubmatch(fileText); m != nil {
		return []string{m[1] + m[2]}, true
	}
	return nil, false
}

// requireDefaultPropagator pins the assumption behind traceHeaders:
// frontends/web/src never installs its own propagator, so the SDK's default
// (W3C trace-context and baggage) is the one that injects headers. A custom or
// composite propagator added here would inject headers this test does not know
// to check.
func requireDefaultPropagator(t *testing.T, webSrc string) {
	t.Helper()
	var installs []string
	err := filepath.WalkDir(webSrc, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "gen", "__tests__", "__mocks__", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !isHandWrittenTS(d.Name()) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(src), "setGlobalPropagator") {
			rel, _ := filepath.Rel(webSrc, path)
			installs = append(installs, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scanning frontends/web/src for propagators: %v", err)
	}
	if len(installs) != 0 {
		t.Fatalf("%v installs a global propagator; update traceHeaders "+
			"with every header it injects", installs)
	}
}

func headerListContains(list, name string) bool {
	for _, item := range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(item), name) {
			return true
		}
	}
	return false
}

func lineOf(text string, offset int) string {
	return strconv.Itoa(strings.Count(text[:offset], "\n") + 1)
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
