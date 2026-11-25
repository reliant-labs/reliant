package benefits

import (
	"math/big"
	"strings"
	"testing"
)

// hostingCatalog is the pay-as-you-go hosting list in control-plane's
// internal/plansconfig/pricing.yaml (pricing v2, 2026-11-06), the catalog
// reliantlabs.io/pricing renders: each compute plan's cpu/memory/storage
// overage rates, and its included_cpu_hours, included_memory_gib_hours and
// included_storage_gib_hours, which are 0 on every plan. A change there must
// change the claim and this table together.
var hostingCatalog = struct {
	cpuPerVCPUHour, memPerGiBHour, storagePerGiBMonth         string // dollars
	includedCPUHours, includedMemGiBHours, includedStorageGiB int64
}{"0.0455", "0.01375", "0.18", 0, 0, 0}

// TestHostingClaimMatchesTheCatalog pins the hosting-pay-as-you-go claim to
// the catalog: the rates it quotes, that it states the (zero) included CPU,
// memory and storage allowance, and that its worked monthly figures bill
// from the first unit at 730 hours.
func TestHostingClaimMatchesTheCatalog(t *testing.T) {
	reg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	var claim string
	for _, b := range reg.Benefits {
		if b.ID == "hosting-pay-as-you-go" {
			claim = b.Claim
		}
	}
	if claim == "" {
		t.Fatal("no hosting-pay-as-you-go benefit")
	}
	c := hostingCatalog
	for _, want := range []string{
		"$" + c.cpuPerVCPUHour + " per vCPU-hour",
		"$" + c.memPerGiBHour + " per GiB-hour",
		"$" + c.storagePerGiBMonth + " per GiB-month",
		"No CPU, memory or storage is included",
	} {
		if !strings.Contains(claim, want) {
			t.Errorf("hosting claim is missing %q:\n%s", want, claim)
		}
	}

	rat := func(s string) *big.Rat {
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			t.Fatalf("bad decimal %q", s)
		}
		return r
	}
	// month bills vcpu and memGiB for 730 hours, less the included hours,
	// plus storageGiB for the month less the included storage.
	month := func(vcpu, memGiB, storageGiB string) string {
		billed := func(qty string, hours, included int64) *big.Rat {
			used := new(big.Rat).Mul(rat(qty), big.NewRat(hours, 1))
			used.Sub(used, big.NewRat(included, 1))
			if used.Sign() < 0 {
				used.SetInt64(0)
			}
			return used
		}
		total := new(big.Rat).Mul(billed(vcpu, 730, c.includedCPUHours), rat(c.cpuPerVCPUHour))
		total.Add(total, new(big.Rat).Mul(billed(memGiB, 730, c.includedMemGiBHours), rat(c.memPerGiBHour)))
		total.Add(total, new(big.Rat).Mul(billed(storageGiB, 1, c.includedStorageGiB), rat(c.storagePerGiBMonth)))
		return "$" + total.FloatString(2)
	}
	for _, want := range []string{month("0.1", "0.5", "0"), month("0.25", "1", "10")} {
		if !strings.Contains(claim, want+" ") {
			t.Errorf("hosting claim does not quote the catalog's %s a month:\n%s", want, claim)
		}
	}
}
