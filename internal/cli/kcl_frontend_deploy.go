package cli

// Frontend runtimes — the Go side of kcl/schema.k `Frontend.runtime`
// (forge.OnHost | OnHosted | OnBucket | OnFirebase | BuildOnly), ADR 0002 §6.
//
// Split out of kcl_render.go because they are a coherent cluster with one
// job — decoding the frontend runtime discriminator and its variant bodies —
// and because kcl_render.go had grown past the public-struct ceiling revive
// enforces. Every type here is decoded from the rendered KCL JSON.

import (
	"encoding/json"
	"fmt"
)

// Frontend runtime discriminators (FrontendRuntime.Type). The dispatch table
// is keyed on exactly these; a typo at one call site would silently skip a
// frontend, so every site names the constant.
const (
	// FrontendRuntimeHost is the dev server (`<dev_runner> dev`). A deploy
	// ships nothing for it.
	FrontendRuntimeHost = "host"
	// FrontendRuntimeHosted is platform static hosting: a release artifact
	// plus a StaticSite CR published to the control plane.
	FrontendRuntimeHosted = "hosted"
	// FrontendRuntimeBucket is the author's own object-storage bucket.
	FrontendRuntimeBucket = "bucket"
	// FrontendRuntimeFirebase is Firebase Hosting.
	FrontendRuntimeFirebase = "firebase"
	// FrontendRuntimeBuildOnly is built by a deploy, never shipped: its
	// output exists for a sibling frontend's `bundle`.
	FrontendRuntimeBuildOnly = "build-only"
)

// FrontendRuntime is a frontend's resolved runtime. Type carries the tag;
// Bucket / Firebase are non-nil exactly when Type names them. host, hosted
// and build-only carry no fields on a frontend.
type FrontendRuntime struct {
	Type     string
	Bucket   *BucketRuntime
	Firebase *FirebaseRuntime
}

// Publishes reports whether a deploy builds this frontend's static output:
// every runtime except the dev server.
func (r FrontendRuntime) Publishes() bool {
	return r.Type != "" && r.Type != FrontendRuntimeHost
}

// Ships reports whether a deploy ships the built site somewhere forge
// publishes to from this machine (a bucket or Firebase) — the out-of-band
// frontend providers. Hosted ships through the control plane instead, and
// build-only ships nowhere.
func (r FrontendRuntime) Ships() bool {
	return r.Type == FrontendRuntimeBucket || r.Type == FrontendRuntimeFirebase
}

// BucketRuntime mirrors forge.OnBucket: WHERE the assembled tree goes. The
// build and assembly inputs (public_dir, base_path, bundle, cache_control)
// are the frontend's own (FrontendEntity).
type BucketRuntime struct {
	Bucket string         `json:"bucket"`
	CDN    *StaticSiteCDN `json:"cdn,omitempty"`
	// KeepReleases has NO omitempty, deliberately. 0 is a meaningful
	// value ("retain everything, never prune"), and the KCL renderer
	// projects the key unconditionally so it survives the round trip. An
	// omitempty here would drop it and the provider would read the
	// absent key back as the default 10, pruning archived releases the
	// author explicitly asked forge to keep — in a bucket, where the
	// loss is not recoverable.
	KeepReleases int `json:"keep_releases"`
}

// FirebaseRuntime mirrors forge.OnFirebase.
type FirebaseRuntime struct {
	Project  string           `json:"project"`
	Site     string           `json:"site"`
	Target   string           `json:"target,omitempty"`
	Rewrites []map[string]any `json:"rewrites,omitempty"`
	// SPAFallback is the entry document served for client-side routes
	// ("/index.html"); empty means no SPA fallback.
	SPAFallback string `json:"spa_fallback,omitempty"`
	// KeepAssetReleases is how many releases' hashed assets the live site
	// serves. The render projects it unconditionally (default 10, floor 2).
	KeepAssetReleases int `json:"keep_asset_releases"`
}

// CacheRule is one Cache-Control header applied to the objects a glob
// matches. Order is significant: first match wins.
type CacheRule struct {
	Pattern      string `json:"pattern"`
	CacheControl string `json:"cache_control"`
}

// StaticSiteCDN is the CDN in front of a bucket and the per-deploy
// invalidation policy. Absent (nil) means the bucket is served directly and
// a deploy invalidates nothing.
type StaticSiteCDN struct {
	URLMap               string   `json:"url_map"`
	Invalidate           string   `json:"invalidate,omitempty"`
	ExtraInvalidatePaths []string `json:"extra_invalidate_paths,omitempty"`
}

// BundleDir is one extra pre-built static directory assembled into a
// published site alongside the frontend's own build output. Dest empty
// means the site root. Runtime-neutral: every static runtime assembles it
// the same way.
type BundleDir struct {
	Src  string `json:"src"`
	Dest string `json:"dest,omitempty"`
}

// UnmarshalJSON dispatches the runtime by its `type` discriminator. An
// unknown type is an error: the render and this binary disagree about the
// contract, and a frontend skipped by a stale dispatch table would ship
// nowhere without anyone noticing.
func (r *FrontendRuntime) UnmarshalJSON(data []byte) error {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	r.Type = probe.Type
	switch probe.Type {
	case FrontendRuntimeHost, FrontendRuntimeHosted, FrontendRuntimeBuildOnly:
	case FrontendRuntimeBucket:
		var b BucketRuntime
		if err := json.Unmarshal(data, &b); err != nil {
			return fmt.Errorf("parse bucket frontend runtime: %w", err)
		}
		r.Bucket = &b
	case FrontendRuntimeFirebase:
		var fb FirebaseRuntime
		if err := json.Unmarshal(data, &fb); err != nil {
			return fmt.Errorf("parse firebase frontend runtime: %w", err)
		}
		r.Firebase = &fb
	default:
		return fmt.Errorf("frontend runtime type %q is not one this forge knows (host | hosted | bucket | firebase | build-only): the render and the binary disagree — use the forge that rendered it", probe.Type)
	}
	return nil
}
