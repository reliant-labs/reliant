// Package ocitest is an in-process OCI distribution registry for tests of
// code that pushes and reads OCI artifacts and their referrers with oras-go.
// It serves only the routes oras-go's remote.Repository uses, over plain
// HTTP on 127.0.0.1, and needs no network and no container.
//
//forge:exclude-contract: a test double: one concrete fake registry, constructed by tests; there is no second implementation to abstract over
package ocitest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Registry is an in-process OCI distribution registry: just the routes
// oras-go's remote.Repository uses to push and read manifests, blobs and
// referrers. It plays both kinds of registry phase 0 found:
//
//   - ReferrersAPI false: distribution v3 / GHCR / Reliant's hosted registry —
//     no /referrers/ route (404), no OCI-Subject header, so the writer keeps
//     the `sha256-<hex>` fallback index itself;
//   - ReferrersAPI true: GAR / zot — the registry indexes `subject` and serves
//     /referrers/, with NullManifests reproducing GAR's `"manifests": null`.
//
// RefuseDelete is the hosted registry's tenant token (no DELETE).
// LoseIndexWrites makes that many fallback-tag writes vanish — the tag keeps
// its old index, exactly what a concurrent writer in another process does to
// the read-modify-write — so verify-and-reattach can be observed.
type Registry struct {
	t   testing.TB
	srv *httptest.Server

	ReferrersAPI    bool
	NullManifests   bool
	RefuseDelete    bool
	loseIndexWrites int
	// CorruptBlob serves different bytes for this digest.
	CorruptBlob digest.Digest

	mu        sync.Mutex
	manifests map[string]map[digest.Digest]storedManifest // repo → digest → manifest
	tags      map[string]map[string]digest.Digest
	blobs     map[digest.Digest][]byte
	uploads   int
	requests  []string
	deletes   int
}

type storedManifest struct {
	mediaType string
	body      []byte
}

// New starts a registry, configured by configure (nil: no referrers API,
// DELETE allowed), and stops it when the test ends.
func New(t testing.TB, configure func(*Registry)) *Registry {
	t.Helper()
	f := &Registry{
		t:         t,
		manifests: map[string]map[digest.Digest]storedManifest{},
		tags:      map[string]map[string]digest.Digest{},
		blobs:     map[digest.Digest][]byte{},
	}
	if configure != nil {
		configure(f)
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// Host is the registry's host:port, a 127.0.0.1 address (plain HTTP).
func (f *Registry) Host() string { return strings.TrimPrefix(f.srv.URL, "http://") }

// PutSubject stores a one-layer artifact manifest under tag in repo and
// returns `<host>/<repo>@<digest>`, the subject reference an attach takes.
func (f *Registry) PutSubject(repo, tag string) string {
	f.t.Helper()
	layer := []byte("layer of " + repo + ":" + tag)
	m := ocispec.Manifest{
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: "application/vnd.forge.staticsite.v1",
		Config:       ocispec.DescriptorEmptyJSON,
		Layers: []ocispec.Descriptor{{
			MediaType: "application/vnd.forge.staticsite.layer.v1.tar+gzip",
			Digest:    digest.FromBytes(layer), Size: int64(len(layer)),
		}},
	}
	m.SchemaVersion = 2
	raw, err := json.Marshal(m)
	if err != nil {
		f.t.Fatal(err)
	}
	d := digest.FromBytes(raw)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blobs[digest.FromBytes(layer)] = layer
	f.repoManifests(repo)[d] = storedManifest{mediaType: ocispec.MediaTypeImageManifest, body: raw}
	f.repoTags(repo)[tag] = d
	return f.Host() + "/" + repo + "@" + d.String()
}

func (f *Registry) repoManifests(repo string) map[digest.Digest]storedManifest {
	if f.manifests[repo] == nil {
		f.manifests[repo] = map[digest.Digest]storedManifest{}
	}
	return f.manifests[repo]
}

func (f *Registry) repoTags(repo string) map[string]digest.Digest {
	if f.tags[repo] == nil {
		f.tags[repo] = map[string]digest.Digest{}
	}
	return f.tags[repo]
}

// Tags is the tags repo holds.
func (f *Registry) Tags(repo string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for t := range f.tags[repo] {
		out = append(out, t)
	}
	return out
}

// Requests is every request served so far, as "METHOD /path".
func (f *Registry) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// DeleteCount is how many DELETEs were asked for, refused or not.
func (f *Registry) DeleteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deletes
}

// LoseIndexWrites makes the next n writes of an existing `sha256-<hex>`
// fallback tag vanish: the tag keeps its old index.
func (f *Registry) LoseIndexWrites(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loseIndexWrites = n
}

// IndexWritesToLose is how many lost writes LoseIndexWrites still owes.
func (f *Registry) IndexWritesToLose() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loseIndexWrites
}

func writeErr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"errors":[{"code":%q,"message":%q}]}`, code, strings.ToLower(code))
}

func (f *Registry) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.mu.Unlock()

	p := r.URL.Path
	if p == "/v2/" || p == "/v2" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if !strings.HasPrefix(p, "/v2/") {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(p, "/v2/")
	for _, route := range []struct {
		sep string
		fn  func(http.ResponseWriter, *http.Request, string, string)
	}{
		{"/blobs/uploads/", f.serveUpload},
		{"/blobs/", f.serveBlob},
		{"/manifests/", f.serveManifest},
		{"/referrers/", f.serveReferrers},
	} {
		if i := strings.LastIndex(rest, route.sep); i > 0 {
			route.fn(w, r, rest[:i], rest[i+len(route.sep):])
			return
		}
	}
	http.NotFound(w, r)
}

func (f *Registry) serveUpload(w http.ResponseWriter, r *http.Request, repo, id string) {
	switch r.Method {
	case http.MethodPost:
		f.mu.Lock()
		f.uploads++
		n := f.uploads
		f.mu.Unlock()
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%d", repo, n))
		w.WriteHeader(http.StatusAccepted)
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		want, err := digest.Parse(r.URL.Query().Get("digest"))
		if err != nil || digest.FromBytes(body) != want {
			writeErr(w, http.StatusBadRequest, "DIGEST_INVALID")
			return
		}
		f.mu.Lock()
		f.blobs[want] = body
		f.mu.Unlock()
		w.Header().Set("Location", "/v2/"+repo+"/blobs/"+want.String())
		w.Header().Set("Docker-Content-Digest", want.String())
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *Registry) serveBlob(w http.ResponseWriter, r *http.Request, _, ref string) {
	d, err := digest.Parse(ref)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "DIGEST_INVALID")
		return
	}
	f.mu.Lock()
	data, ok := f.blobs[d]
	if ok && d == f.CorruptBlob {
		data = bytes.ToUpper(data)
	}
	f.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "BLOB_UNKNOWN")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.Header().Set("Docker-Content-Digest", d.String())
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

func (f *Registry) serveManifest(w http.ResponseWriter, r *http.Request, repo, ref string) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		f.mu.Lock()
		d, m, ok := f.lookup(repo, ref)
		f.mu.Unlock()
		if !ok {
			writeErr(w, http.StatusNotFound, "MANIFEST_UNKNOWN")
			return
		}
		w.Header().Set("Content-Type", m.mediaType)
		w.Header().Set("Content-Length", fmt.Sprint(len(m.body)))
		w.Header().Set("Docker-Content-Digest", d.String())
		if r.Method == http.MethodGet {
			_, _ = w.Write(m.body)
		}
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		d := digest.FromBytes(body)
		mt := r.Header.Get("Content-Type")
		var parsed struct {
			Subject *ocispec.Descriptor `json:"subject"`
		}
		_ = json.Unmarshal(body, &parsed)
		f.mu.Lock()
		f.repoManifests(repo)[d] = storedManifest{mediaType: mt, body: body}
		if _, err := digest.Parse(ref); err != nil { // a tag
			tags := f.repoTags(repo)
			_, had := tags[ref]
			if strings.HasPrefix(ref, "sha256-") && had && f.loseIndexWrites > 0 {
				f.loseIndexWrites-- // a racing writer's older index won
			} else {
				tags[ref] = d
			}
		}
		f.mu.Unlock()
		if f.ReferrersAPI && parsed.Subject != nil {
			w.Header().Set("OCI-Subject", parsed.Subject.Digest.String())
		}
		w.Header().Set("Docker-Content-Digest", d.String())
		w.Header().Set("Location", "/v2/"+repo+"/manifests/"+d.String())
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		f.mu.Lock()
		f.deletes++
		f.mu.Unlock()
		if f.RefuseDelete {
			writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED")
			return
		}
		f.mu.Lock()
		d, _, ok := f.lookup(repo, ref)
		if ok {
			delete(f.repoManifests(repo), d)
			for t, td := range f.repoTags(repo) {
				if td == d {
					delete(f.repoTags(repo), t)
				}
			}
		}
		f.mu.Unlock()
		if !ok {
			writeErr(w, http.StatusNotFound, "MANIFEST_UNKNOWN")
			return
		}
		w.WriteHeader(http.StatusAccepted)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// lookup resolves a tag or digest; f.mu must be held.
func (f *Registry) lookup(repo, ref string) (digest.Digest, storedManifest, bool) {
	d, err := digest.Parse(ref)
	if err != nil {
		var ok bool
		if d, ok = f.tags[repo][ref]; !ok {
			return "", storedManifest{}, false
		}
	}
	m, ok := f.manifests[repo][d]
	return d, m, ok
}

func (f *Registry) serveReferrers(w http.ResponseWriter, r *http.Request, repo, ref string) {
	if !f.ReferrersAPI {
		// distribution v3: no such route.
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	subject, err := digest.Parse(ref)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "DIGEST_INVALID")
		return
	}
	filter := r.URL.Query().Get("artifactType")
	var list []ocispec.Descriptor
	f.mu.Lock()
	for d, m := range f.manifests[repo] {
		var parsed ocispec.Manifest
		if json.Unmarshal(m.body, &parsed) != nil || parsed.Subject == nil || parsed.Subject.Digest != subject {
			continue
		}
		at := parsed.ArtifactType
		if at == "" {
			at = parsed.Config.MediaType
		}
		if filter != "" && at != filter {
			continue
		}
		list = append(list, ocispec.Descriptor{MediaType: m.mediaType, Digest: d, Size: int64(len(m.body)), ArtifactType: at, Annotations: parsed.Annotations})
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
	if filter != "" {
		w.Header().Set("OCI-Filters-Applied", "artifactType")
	}
	if len(list) == 0 && f.NullManifests {
		_, _ = fmt.Fprintf(w, `{"schemaVersion":2,"mediaType":%q,"manifests":null}`, ocispec.MediaTypeImageIndex)
		return
	}
	idx := ocispec.Index{MediaType: ocispec.MediaTypeImageIndex, Manifests: list}
	idx.SchemaVersion = 2
	_ = json.NewEncoder(w).Encode(idx)
}
