// Package deploy is forge's deploy-tier library: the pure functions that
// operate on the tier specs in pkg/deploy/v1alpha1.
//
//   - RenderWorkloads (workload_render.go) turns an environment's Workload
//     set into the Kubernetes objects that run it, under a Profile. It is the
//     one Kubernetes renderer (ADR 0002 §4). Render (render.go) covers the
//     per-object tiers, StaticSite and ManagedDatabase. Both are PURE and
//     DETERMINISTIC: same input, byte-identical output, nothing derived from
//     the time, the environment or a cluster read. Two executors call them —
//     `forge env deploy` when self-hosted and the control plane's operators
//     when hosted — and if the function could disagree with itself, those
//     executors would fight over the same objects forever.
//   - CheckShapeBand / RatioFloor (shape.go) are the hosted billing rule, in
//     one place.
//   - ObservedStateOf / Converged (observed.go) map observed status onto the
//     deploy ledger's vocabulary.
//
// Nothing here talks to a cluster, a registry, a bucket or the control plane.
// Hosted concerns — per-customer namespaces, hostname allocation, registry
// allowlists, quota, secret materialization — are composed AROUND Render's
// output by the control plane. They never enter this package.
package deploy
