// Copyright (c) 2025 Reliant Labs
package models

import (
	"fmt"
	"slices"
	"time"
)

// RetiredModel records that providers have shut a model down, and the model a
// selector naming it runs on instead.
//
// Deleting a retired model from the catalog is not enough on its own: chats,
// presets and workflow inputs still NAME it, usually pinned to a provider
// ("vertex-gemini-2.5-pro@vertexai"), and a name the catalog does not know
// fails every request. A retirement keeps the name resolving — to Replacement,
// on the same provider — and a send that meets it moves the chat to the
// replacement and says so.
//
// Example YAML:
//
//	retired:
//	  - {model: vertex-gemini-2.5-pro, drivers: [vertexai], replacement: vertex-gemini-3.8-flash, retired_on: "2026-10-20"}
type RetiredModel struct {
	// Model is the retired id. When every provider retired it, it has no
	// definition left; when some still serve it, it keeps its definition
	// minus the retired drivers' mappings.
	//
	// YAML key: model
	Model string `yaml:"model" json:"model"`

	// Drivers are the providers that no longer serve Model. The definition
	// must not map them, and Replacement must map every one, so a chat
	// pinned to Model@<driver> lands on Replacement@<driver>.
	//
	// YAML key: drivers
	Drivers []string `yaml:"drivers" json:"drivers"`

	// Replacement is a defined model: normally the successor the provider
	// recommends.
	//
	// YAML key: replacement
	Replacement string `yaml:"replacement" json:"replacement"`

	// RetiredOn is the provider's published retirement date, YYYY-MM-DD.
	//
	// YAML key: retired_on
	RetiredOn string `yaml:"retired_on" json:"retired_on"`
}

// RetiredModelError is a retired model whose replacement cannot be resolved
// either. It unwraps to the replacement's error, so a replacement no connected
// provider serves still matches drivererrors.ErrNoServableProvider.
type RetiredModelError struct {
	Retirement RetiredModel
	Err        error
}

func (e *RetiredModelError) Error() string {
	return fmt.Sprintf("model %s is retired (replaced by %s): %v", e.Retirement.Model, e.Retirement.Replacement, e.Err)
}

func (e *RetiredModelError) Unwrap() error { return e.Err }

// addRetirement validates and installs one retirement. Like a tag entry, every
// check fails the parse: a retirement that names the wrong replacement, or one
// whose replacement cannot serve the pinned provider, does nothing at runtime
// until the day a chat needs it.
func (r *ModelRegistry) addRetirement(index int, retirement RetiredModel) error {
	if retirement.Model == "" {
		return fmt.Errorf("retired[%d]: missing model", index)
	}
	where := fmt.Sprintf("retired[%d] (%s)", index, retirement.Model)
	if len(retirement.Drivers) == 0 {
		return fmt.Errorf("%s: no drivers; name the providers that retired it", where)
	}
	if _, err := time.Parse(time.DateOnly, retirement.RetiredOn); err != nil {
		return fmt.Errorf("%s: retired_on %q is not YYYY-MM-DD", where, retirement.RetiredOn)
	}
	if retirement.Replacement == retirement.Model {
		return fmt.Errorf("%s: a model cannot replace itself", where)
	}
	replacement, ok := r.byID[retirement.Replacement]
	if !ok {
		return fmt.Errorf("%s: unknown replacement %q", where, retirement.Replacement)
	}
	model, defined := r.byID[retirement.Model]
	for _, driver := range retirement.Drivers {
		if r.retirementOn(retirement.Model, driver) != nil {
			return fmt.Errorf("%s: %s is already retired", where, driver)
		}
		if defined && mapsDriver(model, driver) {
			return fmt.Errorf("%s: still maps %s; drop that mapping, or this retirement", where, driver)
		}
		if !mapsDriver(replacement, driver) {
			return fmt.Errorf("%s: replacement %s has no %s mapping, so a chat pinned to %s@%s has nowhere to go",
				where, replacement.ID, driver, retirement.Model, driver)
		}
	}
	stored := retirement
	stored.Drivers = slices.Clone(retirement.Drivers)
	r.retired[retirement.Model] = append(r.retired[retirement.Model], &stored)
	return nil
}

// IsRetired reports whether id names a model every provider retired: it has no
// definition, but a selector naming it still resolves, to its replacement.
func (r *ModelRegistry) IsRetired(id string) bool {
	_, defined := r.byID[id]
	return !defined && len(r.retired[id]) > 0
}

// retirementOn is the retirement of model on driver, or nil.
func (r *ModelRegistry) retirementOn(model, driver string) *RetiredModel {
	for _, retirement := range r.retired[model] {
		if slices.Contains(retirement.Drivers, driver) {
			return retirement
		}
	}
	return nil
}

// unpinnedRetirement is the retirement that applies to a selector naming model
// with no provider, once the model itself has failed to resolve: the first
// retirement on a provider the user has. A model no provider serves any more
// falls back to its first retirement, so the error the user sees is about the
// replacement they would get, not "model not found".
func (r *ModelRegistry) unpinnedRetirement(model string, availableSet map[string]bool) *RetiredModel {
	retirements := r.retired[model]
	for _, retirement := range retirements {
		if slices.ContainsFunc(retirement.Drivers, func(driver string) bool { return availableSet[driver] }) {
			return retirement
		}
	}
	if _, defined := r.byID[model]; !defined && len(retirements) > 0 {
		return retirements[0]
	}
	return nil
}

// resolveReplacement resolves selector — which named a retired model, pinned to
// driverSuffix or to nothing — as its replacement on the same provider. The
// replacement resolves without following retirements again, so a redirect is
// exactly one hop: a model retired twice names its final replacement directly.
func (r *ModelRegistry) resolveReplacement(selector ModelSelector, retirement *RetiredModel, driverSuffix string, availableSet map[string]bool) (*ResolvedModel, error) {
	next := selector
	next.ID = retirement.Replacement
	if driverSuffix != "" {
		next.ID += "@" + driverSuffix
	} else if len(next.Providers) == 0 {
		// Unpinned, the retired model was served by a provider that retired
		// it; prefer that provider for the replacement too, so the move does
		// not also change whose bill the chat runs on.
		next.Providers = slices.DeleteFunc(slices.Clone(retirement.Drivers), func(driver string) bool { return !availableSet[driver] })
	}
	resolved, err := r.resolveID(next, availableSet, false)
	if err != nil {
		return nil, &RetiredModelError{Retirement: *retirement, Err: err}
	}
	recorded := *retirement
	resolved.Retirement = &recorded
	return resolved, nil
}

// mapsDriver reports whether model has a provider mapping for driver.
func mapsDriver(model *ModelDefinition, driver string) bool {
	return slices.ContainsFunc(model.Providers, func(p ProviderMapping) bool { return p.Driver == driver })
}
