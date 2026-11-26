// Package fluxdelivery is the Go half of kcl/lib/flux_delivery.k: gitless Flux
// delivery through a flux-operator ResourceSet.
//
// A ledger decides which bundle digest a delivery unit runs. Its writer turns
// that decision into one [Input], checks it with [Validate], and writes it into
// a ResourceSet whose template ([Template]) flux-operator renders into the
// unit's OCIRepository and one Kustomization per applier. The control-plane
// hub writes inline inputs ([ResourceSet]); self-hosted forge writes a
// ResourceSetInputProvider whose values are the same map ([Input.Map]).
//
// # What lives here, and why in forge
//
//   - The TEMPLATE, as a generated constant (template_gen.go) of the one
//     string in kcl/lib/flux_delivery.k, so a hub writer in Go and a
//     self-hosted render in KCL emit the same objects. TestTemplateMatchesKCL
//     pins the pair.
//   - The INPUT, typed. Nobody writes the input map by hand: [Input.Map] is
//     its JSON form, field names fixed by tags.
//   - The NAMES of the rendered objects ([BundleObjectName] and friends),
//     moved from control-plane's fluxtenant. The observer reads these objects
//     back by name, so a name composed twice is a name that can disagree, and
//     the template does not derive them: they are inputs, computed here.
//   - VALIDATION ([Validate]): the structural refusals that make a bad input
//     unwritable rather than a green environment over nothing. These are
//     facts about the Flux objects the template emits, so they sit beside it.
//   - An in-process RENDERER ([Render]) that reproduces flux-operator's
//     ResourceSet builder, for a writer's pre-write check and for tests. It
//     never applies anything, and it does not import flux-operator (whose
//     builder is internal/, and whose module is AGPL). The real engine is held
//     to it by an oracle test that runs the pinned `flux-operator build
//     resourceset`.
//
// # The safety properties
//
// Every rendered object carries `fluxcd.controlplane.io/prune: disabled`
// unless its input retires it, and every Kustomization carries
// `spec.deletionPolicy: Orphan` unless the input says teardown. So deleting
// the ResourceSet, emptying its inputs or a render that drops an applier
// orphans Flux objects and never deletes a workload. [CheckRendered] asserts
// these properties on a render; a writer runs it before every write.
package fluxdelivery

//go:generate go run ./internal/gentemplate
