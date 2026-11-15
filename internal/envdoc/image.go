package envdoc

// THE IMAGE A CLUSTER PULLS, for a document env: kcl/render.k `_artifact`,
// `_build_image`, `_check_built_digest`, `_check_image_names_registry` and
// `_spec_image`, kcl/base.k `image_tag` / `workload_image_ref_for`, and
// kcl/lib/images.k `image_ref`, ported to Go.
//
// A workload forge builds and a cluster (or an external tool) pulls is pinned
// per release: the render resolves its `spec.image` from the image the build
// pushes (`build_image`), the env's tag (`-D image_tag`), the digest the push
// captured (`-D image_digests`) and, for a recorded release, the exact
// per-slot reference its snapshot binds (`-D forge_workload_image_digests`,
// with `forge_immutable_release` refusing any slot it does not bind). That is
// what makes a redeploy of a rebuilt image roll its pods, and a promotion
// deploy exactly the bytes the release recorded. A document gets the same
// guarantee from this file, held to the KCL by the parity tests in
// internal/cli (env_document_image_test.go) and internal/envdoc.
//
// What the document states, and what is derived (docs/design/env-document.md
// §3):
//
//   - A built workload's `image` is the repository the build pushes, registry
//     included (KCL's Workload.image: `ghcr.io/acme/api`, or `…/api:e2e` to
//     pin the build's tag). `image` and `build_image` are normalized from it
//     exactly as render.k does.
//   - Its `spec.image` is DERIVED. A document leaves it unset; a stated value
//     is accepted only when it is what a render with no bindings emits (a
//     render saved as a document), and is re-derived like an unset one. Any
//     other stated value is refused: it would deploy something other than
//     what forge builds and pushes.
//   - A prebuilt image (no `build`), a sidecar's and a release input's are
//     STATED in full, tag included; a slot the release snapshot binds is
//     replaced by its pinned reference, and an immutable render refuses a
//     slot it does not bind — the same as KCL. (KCL appends the env tag to an
//     untagged prebuilt image; a document's stated reference is the resolved
//     form, so it is kept.)

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/hostedimage"
)

// imageTagDefaults is kcl/base.k _IMAGE_TAG_DEFAULTS: the tag an env renders
// with when nothing binds one. TestImageTagDefaultsMatchKCL reads the table
// out of kcl/base.k.
var imageTagDefaults = map[string]string{"dev": "latest", "staging": "staging", "prod": "stable"}

// pullsImage is the runtimes whose `spec.image` render.k resolves to a pull
// reference (`_spec_image`).
var pullsImage = map[string]bool{"cluster": true, "external": true}

// pinnedRef is the shape an immutable release requires of every slot
// (kcl/base.k workload_image_ref_for).
var pinnedRef = regexp.MustCompile(`^.+@sha256:[a-f0-9]{64}$`)

// imageTag is the env's tag: what the render binds (`-D image_tag`), else
// the document's own `image_tag` (a render saved as a document records the
// tag it was rendered with), else kcl/base.k image_tag's per-env default.
//
// A binding outranks the stated value, unlike a KCL Bundle that pins its
// image_tag: a document's `image_tag` cannot say whether it is a pin or the
// tag of the render it was saved from, and the default is the common case.
// A build that must keep one tag pins it on the image (`image: …/api:e2e`).
func imageTag(doc map[string]any, env string, opts Options) string {
	if opts.ImageTag != "" {
		return opts.ImageTag
	}
	return statedImageTag(doc, env)
}

// statedImageTag is imageTag with no binding: the tag a render with no
// bindings would carry.
func statedImageTag(doc map[string]any, env string) string {
	if s, _ := doc["image_tag"].(string); s != "" {
		return s
	}
	if t := imageTagDefaults[env]; t != "" {
		return t
	}
	return "latest"
}

// hasTagOrDigest is kcl/lib/images.k has_tag_or_digest: a tag colon is the
// rightmost `:` after the rightmost `/`; a digest is any `@`.
func hasTagOrDigest(image string) bool {
	tail := image[strings.LastIndex(image, "/")+1:]
	return strings.Contains(image, "@") || strings.Contains(tail, ":")
}

// imageRef is kcl/lib/images.k image_ref: a digest-pinned image verbatim; a
// digest captured under the image's key appended (the tag kept); a
// tag-pinned image with none verbatim; else the env tag.
func imageRef(image, tag string, digests map[string]string) string {
	if !strings.Contains(image, "@") {
		if d := digests[image]; d != "" {
			return image + "@" + d
		}
	}
	if hasTagOrDigest(image) {
		return image
	}
	return image + ":" + tag
}

// slotPin is the reference the release snapshot binds to workload's slot, ""
// when it binds none. An immutable render requires one.
func (o Options) slotPin(workload, slot string) (string, error) {
	ref := o.WorkloadPins[workload][slot]
	if o.Immutable && (ref == "" || !pinnedRef.MatchString(ref)) {
		return "", fmt.Errorf("immutable release requires workload/slot pin %s/%s", workload, slot)
	}
	return ref, nil
}

// workloadImageRef is kcl/base.k workload_image_ref_for: the slot's pinned
// reference, else image_ref(image, tag, image_digests).
func (o Options) workloadImageRef(workload, slot, image, tag string) (string, error) {
	ref, err := o.slotPin(workload, slot)
	if err != nil || ref != "" {
		return ref, err
	}
	return imageRef(image, tag, o.ImageDigests), nil
}

// statedRef is a stated reference through the release snapshot: its pin when
// the snapshot binds the slot, else the reference as stated.
func (o Options) statedRef(workload, slot, stated string) (string, error) {
	ref, err := o.slotPin(workload, slot)
	if err != nil || ref != "" {
		return ref, err
	}
	return stated, nil
}

// builtImage normalizes a built workload's `image` / `build_image` as
// render.k _artifact / _build_image derive them from Workload.image, and
// returns the image the build pushes (`build_image`, which is the name
// _spec_image resolves). The problems name the workload.
func builtImage(w map[string]any, name string) (string, []string) {
	declared, _ := w["image"].(string)
	built, _ := w["build_image"].(string)
	if built == "" {
		built = declared
		if built == "" {
			// _artifact with no Workload.image: the build's output name.
			b, _ := w["build"].(map[string]any)
			if built, _ = b["output_name"].(string); built == "" {
				built = name
			}
		}
	}
	if strings.Contains(built, "@") {
		// render.k _check_built_digest.
		return "", []string{fmt.Sprintf("workload %q: image %q pins a digest, but the workload also declares a build. "+
			"A build cannot produce a pre-chosen digest: drop `build` to run that exact image, or drop the digest "+
			"(pin a tag, or leave it unpinned) so the image is the one forge builds; `forge env deploy` pins what the build pushed by digest itself", name, built)}
	}
	repo := hostedimage.Repository(built)
	if declared != "" && declared != repo && declared != built {
		return "", []string{fmt.Sprintf("workload %q: image %q is not the repository of build_image %q: "+
			"state one of them (image is the repository the build pushes, registry included)", name, declared, built)}
	}
	w["image"], w["build_image"] = repo, built
	return built, nil
}

// lowerImages resolves a workload's image references (render.k _spec_image,
// the sidecars' and release inputs' workload_image_ref_for). statedTag is the
// tag a render with no bindings carries (what a saved render's spec.image was
// resolved with), tag the one this render binds.
func lowerImages(w, spec map[string]any, rt, statedTag, tag string, opts Options) []string {
	name, _ := w["name"].(string)
	var problems []string
	fail := func(err error) { problems = append(problems, fmt.Sprintf("workload %q: %v", name, err)) }

	if inputs, ok := w["release_inputs"].(map[string]any); ok {
		for k, v := range inputs {
			s, _ := v.(string)
			ref, err := opts.statedRef(name, "input:"+k, s)
			if err != nil {
				fail(err)
				continue
			}
			inputs[k] = ref
		}
	}
	sidecars, _ := spec["sidecars"].([]any)
	for _, raw := range sidecars {
		s, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		sname, _ := s["name"].(string)
		slot := "container:" + sname
		if native, _ := s["native"].(bool); native {
			slot = "init:" + sname
		}
		img, _ := s["image"].(string)
		ref, err := opts.statedRef(name, slot, img)
		if err != nil {
			fail(err)
			continue
		}
		s["image"] = ref
	}
	if !pullsImage[rt] {
		return problems
	}

	stated, _ := spec["image"].(string)
	if stated == "-" {
		stated = ""
	}
	if hasBuild(w) {
		built, bad := builtImage(w, name)
		if len(bad) > 0 {
			return append(problems, bad...)
		}
		if !hostedimage.HasRegistryHost(built) {
			// render.k _check_image_names_registry.
			return append(problems, fmt.Sprintf("workload %q: image %q names no registry host, and it is bound to a %s runtime that must pull it. "+
				"The registry is part of the image: set the workload's image to the full repository, e.g. `image: ghcr.io/<owner>/%s`", name, built, rt, built))
		}
		ref, err := opts.workloadImageRef(name, "container:"+name, built, tag)
		if err != nil {
			fail(err)
			return problems
		}
		if stated != "" && stated != ref && stated != imageRef(built, statedTag, nil) {
			return append(problems, fmt.Sprintf("workload %q: spec.image %q: forge derives the image of a workload it builds per release "+
				"(from `image`, the env's tag and the digest the build pushed); drop spec.image", name, stated))
		}
		spec["image"] = ref
	} else if stated != "" {
		ref, err := opts.statedRef(name, "container:"+name, stated)
		if err != nil {
			fail(err)
		} else {
			spec["image"] = ref
		}
	}

	return problems
}
