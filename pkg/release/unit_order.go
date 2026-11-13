package release

// The delivery-unit order as WAVES, for an applier that applies one unit's
// workloads, waits for them, and only then applies the units after it.
//
// A Flux-applied bundle states the order as trees chained by dependsOn
// ([PlanTrees]); forge's direct apply states the same declaration as waves:
// every unit in wave N waits for nothing outside waves 0..N-1. Both read the
// same `after` rule ([unitAfter]): a declared `after` is exactly what was
// written, an undeclared one means "after [DefaultDeliveryUnit]", and base is
// after nothing.

import (
	"fmt"
	"sort"
	"strings"
)

// UnitWaves orders the delivery units into waves: the units of a wave depend
// only on units of earlier waves, and every wave is sorted by name. The
// universe is every declared unit plus every unit in present (the units the
// render's objects carry); an empty name in present is [DefaultDeliveryUnit].
//
// base is always in the universe. A dependency on a unit that is neither
// declared nor present is an error (a typo would otherwise silently drop an
// ordering the author wrote), and so is a cycle.
//
// With no declared units and nothing present beyond base, the result is the
// one wave [base]: there is nothing to order.
func UnitWaves(declared []DeliveryUnit, present []string) ([][]string, error) {
	decl := map[string]DeliveryUnit{}
	// base is always a unit: everything that names none is in it, and a
	// --target apply that happens to carry none of its objects must not turn
	// an explicit `after = ["base"]` into an error.
	universe := map[string]bool{DefaultDeliveryUnit: true}
	for _, u := range declared {
		if u.Name == "" {
			return nil, fmt.Errorf("%w: a delivery unit has no name", ErrInvalid)
		}
		if _, dup := decl[u.Name]; dup {
			return nil, fmt.Errorf("%w: delivery unit %q is declared twice", ErrInvalid, u.Name)
		}
		decl[u.Name] = u
		universe[u.Name] = true
	}
	for _, u := range present {
		if u == "" {
			u = DefaultDeliveryUnit
		}
		universe[u] = true
	}

	after := map[string][]string{}
	for u := range universe {
		d, ok := decl[u]
		if !ok || d.After == nil {
			if u != DefaultDeliveryUnit {
				after[u] = []string{DefaultDeliveryUnit}
			}
			continue
		}
		for _, dep := range d.After {
			if dep == u {
				return nil, fmt.Errorf("%w: delivery unit %q is after itself", ErrInvalid, u)
			}
			if !universe[dep] {
				return nil, fmt.Errorf("%w: delivery unit %q is after %q, which nothing declares or renders", ErrInvalid, u, dep)
			}
		}
		after[u] = d.After
	}

	done := map[string]bool{}
	var waves [][]string
	for len(done) < len(universe) {
		var wave []string
		for u := range universe {
			if done[u] {
				continue
			}
			ready := true
			for _, dep := range after[u] {
				if !done[dep] {
					ready = false
					break
				}
			}
			if ready {
				wave = append(wave, u)
			}
		}
		if len(wave) == 0 {
			var stuck []string
			for u := range universe {
				if !done[u] {
					stuck = append(stuck, u)
				}
			}
			sort.Strings(stuck)
			return nil, fmt.Errorf("%w: delivery units form a dependency cycle among %s (a unit that must not wait for %q declares after = [])",
				ErrInvalid, strings.Join(stuck, ", "), DefaultDeliveryUnit)
		}
		sort.Strings(wave)
		for _, u := range wave {
			done[u] = true
		}
		waves = append(waves, wave)
	}
	return waves, nil
}
