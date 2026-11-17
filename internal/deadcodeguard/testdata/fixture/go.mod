// A self-contained module of PLANTED DEFECTS. deadcodeguard's rules are
// asserted against it, so every rule has a fixture that proves it fires and a
// fixture that proves it does not. It has no dependencies on purpose: the
// guard's own tests must not need the network or forge's module graph.
module deadcodeguardfixture

go 1.24
