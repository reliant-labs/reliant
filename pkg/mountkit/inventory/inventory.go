// Package inventory owns the DATA-ONLY service descriptor type and reader
// helpers behind the generated internal/app/inventory_gen.go's
// `var Inventory = []inventory.ComponentInfo{...}`.
//
// # Status (FORGE_SHAPE_REDESIGN §1/§2 — lib-boundary extraction)
//
// The generated inventory_gen.go has TWO surfaces from ONE source:
//
//   - TYPED MOUNTS — the run path. Each service has a generated typed method
//     `(*Services).Mount<Svc>(...)` plus MountAll / MountByName. Those stay
//     GENERATED (their signatures are a consumed contract) — this package is
//     NOT involved on the run path.
//   - DATA-ONLY INVENTORY — introspection ONLY. The generated `Inventory` is
//     a `[]ComponentInfo` of pure descriptors that `forge project map` / `forge
//     audit` and the `services` listing read. The descriptor TYPE and its
//     reader helpers live HERE so the generated file shrinks to data rows.
//
// ComponentInfo carries NO mount closure — mounting is the typed generated
// Mount<Svc> methods. Names here are for DISPLAY and selection, never a
// construction key.
package inventory

// ComponentInfo is one mountable service's DATA-ONLY descriptor. It carries
// NO mount closure — mounting is the typed generated Mount<Svc> methods. This
// is the introspection surface (`forge project map` / `audit` / `services`).
type ComponentInfo struct {
	// Name is the runtime kebab name — DISPLAY + selection only.
	Name string
	// ConnectPath is the fully-qualified Connect service path
	// (e.g. "acme.billing.v1.BillingService") — for introspection.
	ConnectPath string
	// BaseService is the version-INDEPENDENT logical service name (the proto
	// identity with its version segment stripped). Version is the proto API
	// version (e.g. "v1", "v2beta1"), or "" when the service's proto package
	// carries no version segment.
	//
	// VERSION-AWARE SEAM: ConnectPath fuses the version (it embeds e.g.
	// ".v1."), so historically version rode inside service identity — a
	// future "billing.v2" would look like a brand-new service. Recording
	// Version as explicit METADATA here makes a second version an ADDITIVE
	// change: same BaseService, new Version.
	BaseService string
	Version     string
	// Kind is the component kind ("service").
	Kind string
}

// FindByName returns the descriptor whose Name matches name, and true; or the
// zero ComponentInfo and false when no row matches. Name is the runtime kebab
// name — the display/selection key, never a construction key.
func FindByName(inv []ComponentInfo, name string) (ComponentInfo, bool) {
	for _, c := range inv {
		if c.Name == name {
			return c, true
		}
	}
	return ComponentInfo{}, false
}

// GroupByBaseService buckets the inventory by version-independent BaseService,
// preserving each bucket's first-seen order. It is the version-aware seam's
// reader: a future multi-version project (billing.v1 + billing.v2) yields one
// bucket keyed "billing" with two rows; today every bucket has exactly one
// row. Callers that group for display/mount iterate the returned map.
func GroupByBaseService(inv []ComponentInfo) map[string][]ComponentInfo {
	out := make(map[string][]ComponentInfo, len(inv))
	for _, c := range inv {
		out[c.BaseService] = append(out[c.BaseService], c)
	}
	return out
}
