// Copyright (c) 2025 Reliant Labs
package triggerowner

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Each store role is required on its own: leaving any one out refuses
// construction, so a service cannot be built that fails at its first write.
func TestNewRefusesAMissingStoreRole(t *testing.T) {
	full := Deps{Tx: &MockTxRunner{}, Locks: &MockLocks{}, Triggers: &MockTriggers{}, Adoptions: &MockAdoptions{}, Offers: &MockOffers{}, OrgReads: &MockOrgReads{}}
	_, err := New(full)
	require.NoError(t, err)

	for name, mutate := range map[string]func(*Deps){
		"Tx":        func(d *Deps) { d.Tx = nil },
		"Locks":     func(d *Deps) { d.Locks = nil },
		"Triggers":  func(d *Deps) { d.Triggers = nil },
		"Adoptions": func(d *Deps) { d.Adoptions = nil },
		"Offers":    func(d *Deps) { d.Offers = nil },
		"OrgReads":  func(d *Deps) { d.OrgReads = nil },
	} {
		d := full
		mutate(&d)
		_, err := New(d)
		require.Error(t, err, name)
	}
}
