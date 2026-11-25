package sbom

import "context"

// The package's behavioural surface is three seams:
//
//   - [Describer] — artifacts in, one SBOM ref (and bytes) per artifact
//     out. *Generator is the implementation.
//   - [Runner] — how syft is run. The default execs the pinned binary; tests
//     substitute a fake, so no test needs syft, docker or a registry.
//   - [CacheStore] — the machine's SBOM cache. *Cache is the implementation.
//
// Target and Result are data carriers, not behaviour to mock.

// Describer describes artifacts. It never fails: a target it could not
// describe gets an unavailable ref saying why.
type Describer interface {
	Generate(ctx context.Context, targets []Target) []Result
}

// Runner runs syft with args and returns what it wrote to stdout. env is
// added to syft's own environment (KEY=value; a later key wins over the
// inherited one) and may carry a registry credential: a Runner never logs it
// or puts it on a command line.
type Runner interface {
	Run(ctx context.Context, bin string, args, env []string) ([]byte, error)
}

// CacheStore holds generated SBOMs keyed by every input that decides their
// bytes ([CacheKey]).
type CacheStore interface {
	Get(key CacheKey) (data []byte, source string, ok bool)
	Put(key CacheKey, source string, data []byte) error
}

var (
	_ Describer  = (*Generator)(nil)
	_ Runner     = execRunner{}
	_ CacheStore = (*Cache)(nil)
)
