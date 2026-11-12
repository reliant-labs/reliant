package codegen

import (
	"testing"
)

const observeContractSrc = "package x\n\ntype Service interface{\n\t// forge:no-observe\n\tSkip() error\n\tKeep() error\n}\n"

// TestHasConstructorMarker_OnNewDoc: the marker on func New's doc is detected.
func TestHasConstructorMarker_OnNewDoc(t *testing.T) {
	dir := writeObserveDir(t, map[string]string{
		"contract.go": "package x\n\ntype Service interface{ Do() error }\n",
		"service.go":  "package x\n\ntype Deps struct{}\ntype service struct{}\n\n// New builds it.\n// forge:constructor\nfunc New(d Deps) (Service, error) { return &service{}, nil }\n",
	})
	if !HasConstructorMarker(dir) {
		t.Fatal("// forge:constructor on func New doc should be detected")
	}
	if HasPackageNoObserveDirective(dir) {
		t.Fatal("no package-level // forge:no-observe present")
	}
}

// TestPackageNoObserve_MethodMarkerNotPackageLevel: a method-level
// `// forge:no-observe` (on an interface method) must NOT be read as a
// package-level opt-out.
func TestPackageNoObserve_MethodMarkerNotPackageLevel(t *testing.T) {
	dir := writeObserveDir(t, map[string]string{
		"contract.go": observeContractSrc,
		"service.go":  "package x\n\ntype Deps struct{}\ntype service struct{}\n\n// forge:constructor\nfunc New(d Deps) (Service, error) { return &service{}, nil }\n",
	})
	if HasPackageNoObserveDirective(dir) {
		t.Fatal("a METHOD-level // forge:no-observe must not count as a package-level opt-out")
	}
	if !HasConstructorMarker(dir) {
		t.Fatal("constructor marker should still be detected")
	}
}

// TestPackageNoObserve_OnConstructor: package-level opt-out on func New.
func TestPackageNoObserve_OnConstructor(t *testing.T) {
	dir := writeObserveDir(t, map[string]string{
		"contract.go": "package x\n\ntype Service interface{ Do() error }\n",
		"service.go":  "package x\n\ntype Deps struct{}\ntype service struct{}\n\n// forge:no-observe\nfunc New(d Deps) (Service, error) { return &service{}, nil }\n",
	})
	if !HasPackageNoObserveDirective(dir) {
		t.Fatal("// forge:no-observe on func New doc should be a package-level opt-out")
	}
}

// TestShouldInstrumentComponent covers the full decision matrix.
func TestShouldInstrumentComponent(t *testing.T) {
	markerService := "package x\n\ntype Deps struct{}\ntype service struct{}\n\n// forge:constructor\nfunc New(d Deps) (Service, error) { return &service{}, nil }\n"
	seam := "package x\n\nimport \"github.com/reliant-labs/forge/pkg/observe\"\n\nfunc newObserveChain() *observe.ComponentChain { return nil }\n"
	plainService := "package x\n\ntype Deps struct{}\ntype service struct{}\n\nfunc New(d Deps) (Service, error) { return &service{}, nil }\n"
	contract := "package x\n\ntype Service interface{ Do() error }\n"

	cases := []struct {
		name  string
		files map[string]string
		ctor  string
		iface string
		want  bool
	}{
		{
			name:  "marker",
			files: map[string]string{"contract.go": contract, "service.go": markerService},
			ctor:  "Service", iface: "Service", want: true,
		},
		{
			name:  "seam, no marker",
			files: map[string]string{"contract.go": contract, "service.go": plainService, "observe_chain.go": seam},
			ctor:  "Service", iface: "Service", want: true,
		},
		{
			// The default: instrumentation needs no opt-in. A package that
			// predates the scaffold has neither, and was silently untraced
			// while one was required.
			name:  "neither marker nor seam → instrumented by default",
			files: map[string]string{"contract.go": contract, "service.go": plainService},
			ctor:  "Service", iface: "Service", want: true,
		},
		{
			name:  "package no-observe wins over marker+seam",
			files: map[string]string{"contract.go": contract, "service.go": "package x\n\ntype Deps struct{}\ntype service struct{}\n\n// forge:constructor\n// forge:no-observe\nfunc New(d Deps) (Service, error) { return &service{}, nil }\n", "observe_chain.go": seam},
			ctor:  "Service", iface: "Service", want: false,
		},
		{
			name:  "handler *Service constructor never wrapped",
			files: map[string]string{"contract.go": contract, "service.go": markerService, "observe_chain.go": seam},
			ctor:  "*Service", iface: "Service", want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeObserveDir(t, tc.files)
			if got := ShouldInstrumentComponent(dir, tc.ctor, tc.iface); got != tc.want {
				t.Fatalf("ShouldInstrumentComponent = %v, want %v", got, tc.want)
			}
		})
	}
}

// A package returning an interface but without contract.go gets no decorator
// from the contracts step, so it is never instrumented; nor is one carrying
// //forge:exclude-contract.
func TestShouldInstrumentComponent_RequiresAContractPackage(t *testing.T) {
	service := "package x\n\ntype Service interface{ Do() error }\ntype Deps struct{}\ntype service struct{}\n\nfunc New(d Deps) (Service, error) { return &service{}, nil }\n"
	if ShouldInstrumentComponent(writeObserveDir(t, map[string]string{"service.go": service}), "Service", "Service") {
		t.Error("a package with no contract.go was instrumented")
	}
	excluded := "//forge:exclude-contract: built elsewhere\npackage x\n\ntype Service interface{ Do() error }\n"
	if ShouldInstrumentComponent(writeObserveDir(t, map[string]string{"contract.go": excluded, "service.go": service}), "Service", "Service") {
		t.Error("an exclude-contract package was instrumented")
	}
}
