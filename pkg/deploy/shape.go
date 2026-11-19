package deploy

import (
	"fmt"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// THE SHAPE BAND — the ONE definition of the hosted platform's 4 GiB-per-vCPU
// rule.
//
// It used to be defined twice. forge's KCL SimpleBackend check required the
// request to sit exactly on the band. control-plane's
// inframeter.RatioFloor repriced an off-band request to
// max(requested, band-implied) on each axis. Two definitions of one rule
// agree only by luck. The hosted path also never ran forge's KCL, so the
// "enforcement" was bypassed: the live dev cluster ran a SimpleBackend at
// 100m/128Mi.
//
// PLACEMENT (owner decision): the band is a BILLING rule, so it applies to
// HOSTED environments only. It is a function the hosted path calls — forge's
// hosted-provider preflight before publishing, and the control plane's
// operator and meter after — and it is NOT a schema invariant imposed on
// every self-hosted user of the tier. control-plane's RatioFloor should
// become a call to RatioFloor here, and its quota admission a call to
// CheckShapeBand.

// MemGiBPerVCPU is the band: 4 GiB of memory per vCPU. It matches the top of
// the daemon ladder (8 CPU / 32 GiB), so that ladder is already conformant.
const MemGiBPerVCPU = 4

// BandGranularityMillicores is the coarsest CPU step at which the
// band-implied memory lands on a whole byte. It produces the conformant
// ladder 125m/512Mi, 250m/1Gi, 500m/2Gi, 1000m/4Gi, and so on.
const BandGranularityMillicores = 125

const bytesPerGiB = int64(1) << 30

// BandMemoryBytes is the memory the band implies for a CPU request.
func BandMemoryBytes(cpuMillicores int64) int64 {
	return cpuMillicores * MemGiBPerVCPU * bytesPerGiB / 1000
}

// CheckShapeBand reports whether a resource request sits exactly ON the band,
// with a reason naming the conformant shape when it does not.
//
// EQUALITY, not a floor. The platform bills the max of what was asked and what
// the band implies, so an off-band request is not rejected at billing time: it
// is REPRICED silently, and the invoice stops matching the declaration.
// Requiring the request to be on the band at deploy time is what makes the bill
// equal the request. Only the REQUEST pair is checked, because requests are
// what is reserved and billed; limits are the workload's burst ceiling.
//
// Defaults are applied first, so an omitted resources block (the 250m / 1 GiB
// entry shape) passes.
func CheckShapeBand(r v1alpha1.Resources) error {
	r = r.WithDefaults()
	cpu, mem := r.CPURequestMillicores, r.MemoryRequestBytes
	if cpu <= 0 {
		return fmt.Errorf("resources.cpuRequestMillicores must be positive for a hosted environment, got %d", cpu)
	}
	if cpu%BandGranularityMillicores != 0 {
		return fmt.Errorf("resources.cpuRequestMillicores %d must be a multiple of %dm for a hosted environment (125m/250m/500m/1000m/…): the coarsest step at which the %d GiB-per-vCPU band lands on a whole byte", cpu, BandGranularityMillicores, MemGiBPerVCPU)
	}
	if want := BandMemoryBytes(cpu); mem != want {
		return fmt.Errorf("resources (%dm, %d bytes) are off the hosted %d GiB-per-vCPU shape band: %dm needs exactly %d bytes (%s). The platform bills the max of what you ask for and what the band implies, so an off-band shape would be repriced silently rather than rejected",
			cpu, mem, MemGiBPerVCPU, cpu, want, humanBytes(want))
	}
	return nil
}

// RatioFloor rounds UP the deficient axis of a (cpu millicores, memory bytes)
// request to the band. It is the metering half of the same rule: a lopsided
// shape costs what it occupies. It is pure, so admission and metering can call
// the same function.
//
// Bytes, not control-plane's MiB. The neutral unit is bytes everywhere in
// this package, and a MiB-shaped floor would put a unit conversion at every
// call site.
func RatioFloor(cpuMillicores, memoryBytes int64) (int64, int64) {
	impliedMem := BandMemoryBytes(cpuMillicores)
	// Ceiling division: the implied CPU must never round DOWN, or a lopsided
	// memory request would bill for less CPU than it occupies.
	perMilli := int64(MemGiBPerVCPU) * bytesPerGiB
	impliedCPU := (memoryBytes*1000 + perMilli - 1) / perMilli
	return max(cpuMillicores, impliedCPU), max(memoryBytes, impliedMem)
}

func humanBytes(b int64) string {
	switch {
	case b%bytesPerGiB == 0:
		return fmt.Sprintf("%dGi", b/bytesPerGiB)
	case b%(1<<20) == 0:
		return fmt.Sprintf("%dMi", b/(1<<20))
	default:
		return fmt.Sprintf("%d bytes", b)
	}
}
