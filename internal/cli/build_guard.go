package cli

import (
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
)

// goarchToELFMachine maps a Go GOARCH string to the ELF e_machine value a
// correctly cross-compiled `GOOS=linux GOARCH=<arch>` binary must advertise.
// The bool is false for an arch we don't have a mapping for (the guard then
// degrades to an ELF-only check rather than asserting a wrong machine).
//
// Only the arches forge actually targets are listed; extend as needed.
func goarchToELFMachine(goarch string) (elf.Machine, bool) {
	switch goarch {
	case "amd64":
		return elf.EM_X86_64, true
	case "arm64":
		return elf.EM_AARCH64, true
	case "arm":
		return elf.EM_ARM, true
	case "386":
		return elf.EM_386, true
	case "ppc64", "ppc64le":
		return elf.EM_PPC64, true
	case "s390x":
		return elf.EM_S390, true
	case "riscv64":
		return elf.EM_RISCV, true
	default:
		return 0, false
	}
}

// assertLinuxELFBinary is the build-time backstop that guarantees a binary
// destined to be COPYed into a Linux container image is a Linux ELF for the
// expected GOARCH — NOT a native macOS Mach-O / Darwin build, and NOT the
// wrong CPU arch.
//
// It exists because the project image uses the COPY-pattern Dockerfile
// (`COPY bin/<svc>` into distroless, no in-image `RUN go build`): the host
// build's output IS what ships. A wrong-GOOS/GOARCH host binary (e.g. a
// native darwin/arm64 build produced when the arch resolution is skipped)
// otherwise sails straight through `docker build` and only crashes at runtime
// on the node with `exec format error` → CrashLoopBackOff. distroless does no
// arch emulation, so the mismatch is fatal. This guard turns that runtime
// crash into a fail-fast build error with an actionable message.
//
// expectedArch is the resolved image GOARCH (resolveBuildArchForImage). It is
// compared against the ELF header's e_machine. The check:
//   - the file must parse as ELF (a Mach-O / Darwin binary fails here — that's
//     the "host build leaked through" case the bug was about); and
//   - the ELF e_machine must equal the expected arch's machine (when we have a
//     mapping for expectedArch — a cross-arch mismatch, e.g. arm64 binary in an
//     amd64-targeted image).
func assertLinuxELFBinary(path, expectedArch, envLabel string) error {
	f, err := elf.Open(path)
	if err != nil {
		// elf.Open fails on a Mach-O / PE / non-ELF file — the canonical
		// symptom of a host (darwin) build that skipped GOOS=linux. Surface
		// the actionable remedy, not the raw parse error.
		return fmt.Errorf(
			"project image binary %s is not a Linux ELF executable (likely a native macOS Mach-O/Darwin build): %v\n"+
				"  the %s image targets linux/%s, so this binary will fail at runtime with `exec format error` (CrashLoopBackOff).\n"+
				"  the host build must set GOOS=linux GOARCH=%s — this is the project-image arch resolution (resolveBuildArchForImage);\n"+
				"  if you invoked `go build` directly, re-run the build through `forge build` so the cross-compile env is applied",
			path, err, envLabel, expectedArch, expectedArch)
	}
	defer func() { _ = f.Close() }()

	// GOOS=linux Go binaries are ELFCLASS64/32 with OSABI NONE (SysV) — but the
	// ELF format itself is the OS-portable container, so the strongest portable
	// signal that this is NOT a darwin build is simply that it parsed as ELF
	// (Mach-O does not). The e_machine check below pins the CPU arch.
	want, known := goarchToELFMachine(expectedArch)
	if known && f.Machine != want {
		return fmt.Errorf(
			"project image binary %s is a Linux ELF for %s, but the %s image targets linux/%s.\n"+
				"  the COPYed binary's CPU arch must match the image platform or it fails at runtime with `exec format error`.\n"+
				"  the host build must set GOOS=linux GOARCH=%s (resolveBuildArchForImage); pass --target-arch %s to override",
			path, elfMachineGOARCH(f.Machine), envLabel, expectedArch, expectedArch, expectedArch)
	}
	return nil
}

// elfMachineGOARCH renders an ELF e_machine back to a human GOARCH-ish label
// for error messages. Falls back to the raw machine string for arches we don't
// name.
func elfMachineGOARCH(m elf.Machine) string {
	switch m {
	case elf.EM_X86_64:
		return "amd64"
	case elf.EM_AARCH64:
		return "arm64"
	case elf.EM_ARM:
		return "arm"
	case elf.EM_386:
		return "386"
	default:
		return m.String()
	}
}

// assertProjectImageBinaries runs assertLinuxELFBinary over every host-built
// binary the project image will COPY. Called by dockerBuildProject right
// before `docker build`, so a wrong-arch/Mach-O binary fails the build at the
// COPY boundary rather than shipping a broken image. Binaries that don't exist
// on disk are skipped (the Dockerfile may not COPY every declared target, and
// a missing file is a separate, louder failure at docker-build time).
//
// outputDir is where the host go-builds wrote their binaries (opts.outputDir,
// default "bin"); names are the goBuildTarget output basenames. expectedArch
// is the resolved image GOARCH. envLabel is the deploy env for the error text.
func assertProjectImageBinaries(outputDir, expectedArch, envLabel string, names []string) error {
	for _, name := range names {
		p := filepath.Join(outputDir, name)
		if _, err := os.Stat(p); err != nil {
			// Not present in the output dir — either not COPYed by this
			// Dockerfile or built elsewhere. Don't fail the guard on a
			// missing file; docker build will fail loudly if it's required.
			continue
		}
		if err := assertLinuxELFBinary(p, expectedArch, envLabel); err != nil {
			return err
		}
	}
	return nil
}
