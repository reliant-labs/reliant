package deploytarget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Asset retention: a Firebase deploy keeps the previous releases'
// content-hashed assets serveable.
//
// THE PROBLEM. A Firebase Hosting release is a complete snapshot: a file the
// new build does not contain is gone the instant the release goes live. A
// tab opened before the deploy is still running the old build, and the
// first code-split chunk it lazy-loads afterwards names a file that no
// longer exists. That is not an edge case — it is every open tab, on every
// deploy, the first time its user navigates somewhere new.
//
// THE FIX. Each deploy uploads, beside its own build, the hashed assets of
// the releases before it (up to keep_asset_releases releases, the current
// one included). Firebase stores content by hash, so a carried file was
// uploaded once already and costs no upload — only the download forge makes
// to put it back in the tree it hands `firebase deploy`.
//
// WHY A MANIFEST ON THE SITE. Firebase has no "keep these files" switch, and
// a version's file list cannot say which files that release BUILT and which
// it only carried — so retention would never expire anything. Each deploy
// therefore writes forge-assets.json at its mount: every asset the site
// serves, with its sha256 and the newest release that built it. The next
// deploy reads it from the live site, carries what is still inside the
// window, verifies each download against its recorded hash, and writes the
// next manifest. The state lives with the thing it describes, needs no
// credential beyond the deploy's own, and survives a fresh CI runner.
//
// IT DEGRADES, IT DOES NOT BLOCK. An unreadable manifest or an asset that
// cannot be fetched is reported and the deploy proceeds: the cost of a
// missing old chunk is one reload in an open tab (the app recovers from a
// stale chunk), while the cost of a blocked deploy can be an outage that
// lasts until someone notices. Nothing a deploy carries is ever unverified,
// though — a download whose bytes do not match the manifest is dropped, so
// an HTML fallback page can never be republished as a JavaScript chunk.

// AssetManifestName is the file each Firebase deploy writes at its mount
// (`<base_path>/forge-assets.json`) recording the assets the site serves.
const AssetManifestName = "forge-assets.json"

// assetManifestSchema versions the manifest document. A manifest with any
// other schema is read as absent rather than half-trusted.
const assetManifestSchema = "forge.dev/firebase-assets/v1"

// defaultKeepAssetReleases mirrors OnFirebase.keep_asset_releases' default.
const defaultKeepAssetReleases = 10

// maxCarriedAssetBytes bounds one carried download. The largest thing a web
// build emits is a wasm module; anything past this is not a build asset.
const maxCarriedAssetBytes = 256 << 20

// assetFetchConcurrency is how many carried assets download at once.
const assetFetchConcurrency = 8

// assetManifest is forge-assets.json.
type assetManifest struct {
	Schema string `json:"schema"`
	// Release counts this site mount's forge deploys. Retention is decided
	// on it, not on a clock, so a slow week and a busy afternoon keep the
	// same number of releases.
	Release int `json:"release"`
	// DeployedAt is informational: when the release was written.
	DeployedAt string `json:"deployed_at,omitempty"`
	// Assets is every hashed asset the release serves, keyed by its
	// site-root-relative path ("assets/index-abc123.js").
	Assets map[string]assetRecord `json:"assets"`
}

type assetRecord struct {
	SHA256 string `json:"sha256"`
	// Release is the newest release whose OWN build contained the asset.
	// A carried asset keeps the number it had; a rebuilt one is renumbered.
	Release int `json:"release"`
}

// assetRetentionPlan is the resolved retention step for one frontend.
// Empty AssetRel means the frontend has no content-hashed asset directory
// and retention is off.
type assetRetentionPlan struct {
	AssetRel    string // site-root-relative asset dir ("assets", "admin/assets")
	ManifestRel string // site-root-relative manifest path
	SiteURL     string // origin the live release is read from
	Keep        int    // releases whose assets stay serveable, current included
}

func (r assetRetentionPlan) enabled() bool { return r.AssetRel != "" }

func (r assetRetentionPlan) manifestURL() string {
	return r.SiteURL + "/" + escapeSitePath(r.ManifestRel)
}

// effectiveKeep floors keep at 2 (the release going live plus the one tabs
// are running now) and defaults an unset value.
func effectiveKeepAssetReleases(keep int) int {
	switch {
	case keep == 0:
		return defaultKeepAssetReleases
	case keep < 2:
		return 2
	}
	return keep
}

// defaultFirebaseSiteURL is the origin every Firebase Hosting site answers
// on, whatever custom domains it also has.
func defaultFirebaseSiteURL(site string) string {
	return "https://" + site + ".web.app"
}

// retainAssets runs the retention step against an assembled staging tree:
// read the live release's manifest, put the still-retained assets back into
// the tree, and write the next manifest. Only a local I/O failure is an
// error; everything the network can do wrong is reported and absorbed.
func (p FirebaseProvider) retainAssets(ctx context.Context, name, stagingDir string, plan assetRetentionPlan) error {
	if !plan.enabled() {
		return nil
	}
	built, err := hashAssetTree(stagingDir, plan.AssetRel)
	if err != nil {
		return fmt.Errorf("firebase %s: asset retention: %w", name, err)
	}
	if len(built) == 0 {
		fmt.Printf("  [firebase] %s: asset retention: the build has no files under /%s — check the frontend's asset_dir\n", name, plan.AssetRel)
	}

	client := p.httpClient()
	prev, why := p.readAssetManifest(ctx, client, plan)
	if prev == nil {
		fmt.Printf("  [firebase] %s: asset retention: %s — this release cannot keep the live release's assets serveable; the next one will\n", name, why)
	}

	release, carry := planAssetCarry(prev, built, plan.AssetRel, plan.Keep)
	carried, failures := p.fetchCarriedAssets(ctx, client, stagingDir, plan.SiteURL, carry)

	next := assetManifest{
		Schema:     assetManifestSchema,
		Release:    release,
		DeployedAt: time.Now().UTC().Format(time.RFC3339),
		Assets:     make(map[string]assetRecord, len(built)+len(carried)),
	}
	for rel, sum := range built {
		next.Assets[rel] = assetRecord{SHA256: sum, Release: release}
	}
	releases := map[int]bool{}
	for rel, rec := range carried {
		next.Assets[rel] = rec
		releases[rec.Release] = true
	}
	if err := writeAssetManifest(stagingDir, plan.ManifestRel, next); err != nil {
		return fmt.Errorf("firebase %s: asset retention: %w", name, err)
	}

	fmt.Printf("  [firebase] %s: asset retention: release %d serves %d built + %d carried asset(s) from %d earlier release(s) (keep_asset_releases=%d)\n",
		name, release, len(built), len(carried), len(releases), plan.Keep)
	reportCarryFailures(name, failures)
	return nil
}

// planAssetCarry decides which of the live manifest's assets the next
// release carries. Pure: no network, no filesystem.
//
// An asset is carried when the new build does not contain it (a rebuilt
// asset is the new build's), it was built within the last keep releases,
// and its record is well-formed. The manifest arrives over the network, so
// a path that is not a clean path under the asset directory is dropped
// rather than trusted with a write into the staging tree.
func planAssetCarry(prev *assetManifest, built map[string]string, assetRel string, keep int) (int, map[string]assetRecord) {
	carry := map[string]assetRecord{}
	if prev == nil {
		return 1, carry
	}
	release := prev.Release + 1
	for rel, rec := range prev.Assets {
		if _, rebuilt := built[rel]; rebuilt {
			continue
		}
		// A release that predates map extraction may have served maps. They
		// are never carried forward: carrying one would publish it again.
		if strings.HasSuffix(rel, ".map") {
			continue
		}
		if !safeAssetPath(rel, assetRel) || !validSHA256(rec.SHA256) {
			continue
		}
		if rec.Release < 1 || rec.Release > prev.Release || release-rec.Release >= keep {
			continue
		}
		carry[rel] = rec
	}
	return release, carry
}

// readAssetManifest fetches and validates the live release's manifest. A nil
// manifest comes with the reason it is absent.
//
// A site whose live release predates retention answers the manifest's path
// with whatever it answers any missing path — 404, or (behind a catch-all
// SPA rewrite) index.html with a 200. Both mean "no manifest", which is why
// anything that is not this schema's JSON is read as absent.
func (p FirebaseProvider) readAssetManifest(ctx context.Context, client HTTPDoer, plan assetRetentionPlan) (*assetManifest, string) {
	target := plan.manifestURL()
	// The manifest is no-cache, and a deploy purges Firebase's CDN, but a
	// query the CDN has never seen makes "read the live release" literal.
	fetchURL := target + "?forge-release=" + strconv.FormatInt(time.Now().UnixNano(), 36)

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := p.sleep(ctx, time.Second*time.Duration(attempt)); err != nil {
				return nil, fmt.Sprintf("could not read %s (%v)", target, err)
			}
		}
		body, status, contentType, err := httpGet(ctx, client, fetchURL, 16<<20)
		switch {
		case err != nil:
			lastErr = err
			continue
		case status == http.StatusNotFound:
			return nil, "no asset manifest at " + target + " (the live release predates asset retention)"
		case status >= 500:
			lastErr = fmt.Errorf("HTTP %d", status)
			continue
		case status != http.StatusOK:
			return nil, fmt.Sprintf("could not read %s (HTTP %d)", target, status)
		}
		if isHTMLContentType(contentType) {
			return nil, "no asset manifest at " + target + " (the site answered with an HTML page: the live release predates asset retention)"
		}
		var m assetManifest
		if err := json.Unmarshal(body, &m); err != nil || m.Schema != assetManifestSchema || m.Release < 1 {
			return nil, "the document at " + target + " is not a forge asset manifest"
		}
		return &m, ""
	}
	return nil, fmt.Sprintf("could not read %s after 3 attempts (%v)", target, lastErr)
}

type carryFailure struct {
	rel string
	err error
}

// fetchCarriedAssets downloads each carried asset from the live site into
// the staging tree, verifying its bytes against the manifest's sha256. It
// returns the assets actually written and why the others were not.
func (p FirebaseProvider) fetchCarriedAssets(ctx context.Context, client HTTPDoer, stagingDir, siteURL string, carry map[string]assetRecord) (map[string]assetRecord, []carryFailure) {
	rels := make([]string, 0, len(carry))
	for rel := range carry {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		ok       = map[string]assetRecord{}
		failures []carryFailure
		sem      = make(chan struct{}, assetFetchConcurrency)
	)
	for _, rel := range rels {
		rec := carry[rel]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			err := p.fetchCarriedAsset(ctx, client, stagingDir, siteURL, rel, rec.SHA256)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, carryFailure{rel: rel, err: err})
				return
			}
			ok[rel] = rec
		}()
	}
	wg.Wait()
	sort.Slice(failures, func(i, j int) bool { return failures[i].rel < failures[j].rel })
	return ok, failures
}

func (p FirebaseProvider) fetchCarriedAsset(ctx context.Context, client HTTPDoer, stagingDir, siteURL, rel, wantSHA string) error {
	target := siteURL + "/" + escapeSitePath(rel)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			if err := p.sleep(ctx, time.Second); err != nil {
				return err
			}
		}
		body, status, _, err := httpGet(ctx, client, target, maxCarriedAssetBytes)
		switch {
		case err != nil:
			lastErr = err
			continue
		case status >= 500:
			lastErr = fmt.Errorf("HTTP %d", status)
			continue
		case status != http.StatusOK:
			// 404: the live release no longer has it (it was never
			// carried into the release now live). Not retryable.
			return fmt.Errorf("HTTP %d", status)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != wantSHA {
			return fmt.Errorf("content does not match the manifest (sha256 %s, want %s)", got[:12], wantSHA[:12])
		}
		dst := filepath.Join(stagingDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return atomicfile.WriteFile(dst, body, 0o644)
	}
	return lastErr
}

func reportCarryFailures(name string, failures []carryFailure) {
	const shown = 10
	for i, f := range failures {
		if i == shown {
			fmt.Printf("  [firebase] WARNING %s: asset retention: …and %d more asset(s) could not be carried\n", name, len(failures)-shown)
			return
		}
		fmt.Printf("  [firebase] WARNING %s: asset retention: could not carry /%s: %v\n", name, f.rel, f.err)
	}
}

// hashAssetTree returns the sha256 of every file under stagingDir/assetRel,
// keyed by site-root-relative slash path. A missing directory is empty.
func hashAssetTree(stagingDir, assetRel string) (map[string]string, error) {
	out := map[string]string{}
	root := filepath.Join(stagingDir, filepath.FromSlash(assetRel))
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(stagingDir, p)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, cerr := io.Copy(h, f)
		_ = f.Close()
		if cerr != nil {
			return cerr
		}
		out[filepath.ToSlash(rel)] = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("hash /%s: %w", assetRel, err)
	}
	return out, nil
}

func writeAssetManifest(stagingDir, manifestRel string, m assetManifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	dst := filepath.Join(stagingDir, filepath.FromSlash(manifestRel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(dst, append(b, '\n'), 0o644)
}

// safeAssetPath reports whether rel is a clean, relative slash path strictly
// under assetRel.
func safeAssetPath(rel, assetRel string) bool {
	if rel == "" || path.Clean(rel) != rel || path.IsAbs(rel) || strings.Contains(rel, `\`) {
		return false
	}
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return false
	}
	return strings.HasPrefix(rel, assetRel+"/")
}

func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func isHTMLContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && (mt == "text/html" || mt == "application/xhtml+xml")
}

// escapeSitePath percent-encodes each segment of a site-relative path.
func escapeSitePath(rel string) string {
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// httpGet GETs target and returns at most limit bytes of its body. A body
// longer than limit is an error, never a silently truncated file.
func httpGet(ctx context.Context, client HTTPDoer, target string, limit int64) ([]byte, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, resp.StatusCode, resp.Header.Get("Content-Type"), nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, 0, "", err
	}
	if int64(len(body)) > limit {
		return nil, 0, "", fmt.Errorf("larger than %d bytes", limit)
	}
	return body, resp.StatusCode, resp.Header.Get("Content-Type"), nil
}

func (p FirebaseProvider) httpClient() HTTPDoer {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func (p FirebaseProvider) sleep(ctx context.Context, d time.Duration) error {
	if p.Sleep != nil {
		return p.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (p FirebaseProvider) siteURL(site string) string {
	if p.SiteURL != nil {
		return strings.TrimRight(p.SiteURL(site), "/")
	}
	return defaultFirebaseSiteURL(site)
}
