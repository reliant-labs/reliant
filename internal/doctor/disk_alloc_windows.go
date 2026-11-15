package doctor

import "io/fs"

// allocatedBytes has no Windows implementation, and that costs nothing:
// Docker.raw is a macOS artefact, so the only caller never asks on this
// platform. Returning false rather than fi.Size() is deliberate — the
// apparent size of a sparse disk image is not a disk reading, and a
// plausible wrong number is worse than no number.
func allocatedBytes(fs.FileInfo) (uint64, bool) { return 0, false }
