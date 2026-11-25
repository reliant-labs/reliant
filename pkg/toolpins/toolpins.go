// Package toolpins is the single source of truth for the non-Go tools forge
// installs as pinned upstream release binaries: syft (SBOM generation) and
// grype (vulnerability scanning). For each one it records the name, the
// release version, where releases are published, and every release archive
// forge has a sha256 for, per GOOS/GOARCH.
//
// forge itself installs these through its tool cache (`forge tools install
// syft grype`, or on first use); this package is for everything else that
// must run the SAME binary forge pins — a worker image that scans SBOMs
// forge uploaded (control-plane), a CI step, a Dockerfile generator:
//
//	g := toolpins.Grype()
//	url, _ := g.ReleaseURL("linux", "amd64")
//	sum, _ := g.SHA256("linux", "amd64")
//	bin, _ := g.BinarySHA256("linux", "amd64")
//	// download url, verify sum, extract g.Binary("linux"), verify it is bin
//
// # Compatibility
//
// This package is part of forge's public API and is held to forge's v1
// semver: changes are additive only. Fields and functions are never removed
// or renamed, and an existing field never changes meaning. Data changes are
// not API changes: a new version of a tool (with new archives and sums), a
// platform gaining or losing an archive upstream, or a new tool in All are
// routine and ship in any release. A consumer that pins a forge version
// therefore pins these tool versions with it; one that must not move pins
// forge.
//
// The values are plain data with no dependency on forge's internals. Every
// accessor returns a fresh copy, so a consumer can never change what forge,
// or another consumer in the same process, sees.
package toolpins

import "strings"

// The pinned versions, as release tags. They are also Syft().Version and
// Grype().Version.
const (
	SyftVersion  = "v1.57.2"
	GrypeVersion = "v0.122.1"
)

// DefaultBaseURL is where releases are published. ReleaseURL joins it with
// ReleaseURLPattern; a mirror of github.com serves the same paths under its
// own base (ReleaseURLFrom).
const DefaultBaseURL = "https://github.com"

// ReleaseURLPattern is the path of an archive under a base URL. {repo} is
// Tool.Repo, {version} is Tool.Version and {archive} is Archive.Name.
const ReleaseURLPattern = "{base}/{repo}/releases/download/{version}/{archive}"

// Archive formats.
const (
	FormatTarGz = "tar.gz"
	FormatZip   = "zip"
)

// Tool is one pinned upstream release.
type Tool struct {
	// Name is the tool's name and its executable's base name: "grype".
	Name string `json:"name"`
	// Version is the release tag: "v0.122.1". Archive names carry it without
	// the leading "v", as goreleaser writes them.
	Version string `json:"version"`
	// Repo is the GitHub owner/repo the release is published under:
	// "anchore/grype".
	Repo string `json:"repo"`
	// GoPackage is the main package that builds the tool with
	// `go install <GoPackage>@<Version>`, for a platform with no archive.
	GoPackage string `json:"go_package"`
	// Archives is every release archive forge has a sha256 for, one per
	// platform, sorted by GOOS then GOARCH. A platform with none here has no
	// verified binary.
	Archives []Archive `json:"archives"`
}

// Archive is one release archive of a Tool, for one platform.
type Archive struct {
	// GOOS and GOARCH name the platform, in Go's spelling: "linux", "amd64".
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	// Name is the release asset's file name:
	// "grype_0.122.1_linux_amd64.tar.gz".
	Name string `json:"name"`
	// Format is FormatTarGz or FormatZip.
	Format string `json:"format"`
	// SHA256 is the archive's sha256, lower-case hex, copied from the
	// release's own checksums file.
	SHA256 string `json:"sha256"`
	// BinarySHA256 is the sha256 of the executable inside the archive
	// (Binary), lower-case hex. Upstream publishes no such sum: forge
	// derived it by downloading the archive, verifying it against SHA256
	// and hashing the member. A consumer that caches the extracted binary
	// can check it against this value without trusting its own cache.
	BinarySHA256 string `json:"binary_sha256"`
	// Binary is the executable's file name inside the archive: the tool's
	// name, plus ".exe" on Windows.
	Binary string `json:"binary"`
}

// pinsHowTo: how the pins below were made, and how to remake them on a bump.
//
//   - Archive SHA256: copied verbatim from the release's
//     <tool>_<ver>_checksums.txt, archives only. testdata/ holds that file;
//     TestPinsMatchTheUpstreamChecksumsFiles holds every pin to it.
//   - Archive BinarySHA256: every pinned archive was downloaded once from
//     its ReleaseURL, checked against its SHA256 (`sha256sum`, all matched),
//     and its executable hashed: `tar xzOf <archive> <name> | sha256sum`, or
//     `unzip -p <archive> <name>.exe | sha256sum` for a .zip. forge's
//     installer refuses an archive whose executable misses this value, and
//     internal/toolfetch's TestPinnedBinaryChecksumsMatchTheirArchives
//     (FORGE_TEST_TOOLFETCH_LIVE=1) re-derives every one from the release.
//
// Syft returns the pinned syft release.
func Syft() Tool {
	return tool("syft", SyftVersion, "anchore/syft", "github.com/anchore/syft/cmd/syft",
		// Each row: GOOS, GOARCH, the archive's sha256 from
		// syft_1.57.2_checksums.txt (archives only, not the .deb, .rpm or
		// .sbom assets), then its executable's sha256 (see pinsHowTo).
		"darwin", "amd64", "73c6100bc43fbcc1c99aed3428d859c6289c4076be7e296dde7e427324c15908", "937b74689e65014b1f5720ded9b3bbf6a464c0e3f50f1c162cbaf3373199bd8e",
		"darwin", "arm64", "38afbcc03a9d9afac2d7572432fd2c435e051e03930582ea90f4710ffb515224", "a55d46242c69903c31c10fdceb4579a9f712280b6994001949f8ef634558a99f",
		"linux", "amd64", "ad92bd3ec1b94a3df01d4d0aec199afa9c42b72e86da436ff5f837914d32706b", "17cc40c1e40dbfdeb8c84b6d9928909bceb793f22e8a8023ef408625f1d96ec5",
		"linux", "arm64", "8efe605309c98798e1ee0cffdcf8c05ca26819b4cd44c0b03650f1e963e9bdb6", "2bbe674996bbe831dcc68fa50b0c5e68acd728c5d9da7cc68d88713a9ef6b748",
		"linux", "ppc64le", "9475031e14788f78006c63660a657671470cae841d1f221ad84d7f69c20366aa", "a3ca5d637bfbbeab5950ba329a102dae6a4b33b637fd0b66e7509556ddf0755f",
		"linux", "riscv64", "b1c5ac17db3779f91d01a0583a78c031fa971bdbb5ea3fb1665f78722debff56", "15415fcac4d1a52dbb8fe1ad3925331eb0136b58514a7578670e5407840a526a",
		"linux", "s390x", "87267ac3bcdd7c4545a5715c811cd9a96d76a06a9788b8508a17f5cf1545c280", "9442f0ba30d06d1ae9e6809e764cc595310b43bef232283a09f385c2d2914782",
		"windows", "amd64", "0a10e03ffd124c304fd7e7491262dba2e5f68eb12378b106d7525146675b2e3a", "5c9d2ba5affae29c3c5ba5796ad28fc72780189fafcb893544325902a298b17e",
		"windows", "arm64", "7f3cace29c2a7d2478de834aaa42663b8dbf82c9e8abc6a0917b437584ea54e1", "fc0dc146da865c78c3be317bbdfaa2d28e90dd80594c39cccd56ec5603c82621",
	)
}

// Grype returns the pinned grype release. grype publishes no linux/riscv64
// or windows/arm64 archive.
func Grype() Tool {
	return tool("grype", GrypeVersion, "anchore/grype", "github.com/anchore/grype/cmd/grype",
		// Each row: GOOS, GOARCH, the archive's sha256 from
		// grype_0.122.1_checksums.txt (archives only), then its executable's
		// sha256 (see pinsHowTo).
		"darwin", "amd64", "8063aef11b52e6da342068aa6980e92f40b28849eff990c8375a0a8e6b6dfbc5", "ca82c975f0788c9fa5a882c5eaa4b848e9d2c7c7d28d20519eadfe3cdc9a22f4",
		"darwin", "arm64", "8bbb3c01695d1e954875cf9f7403c5d3cdb652caf763867955fad520e3cd6c00", "df8effdd7d4069cfd8762516aa37ddc9e5bf026469f4f509daae2ef730c517f1",
		"linux", "amd64", "f1ba7e2917ee1395618de20a98da0aa7c0fb1788927797651047c50502a37538", "322a7839c1c9f6c1b6ac0aa6b61a7d94139b7f24780b82a05b63caab56d4286b",
		"linux", "arm64", "819e5be3d9516432fedb7b6b56df7306c3a16607606dfc05826eefbb00e471da", "bd313717cfe7fa6807fed57942b661b02aa1709c77faaf3079a9b217b3a3d98c",
		"linux", "ppc64le", "b85faf0e6770b39758a5803e6517d6c6d4e57a5fe8e09f33ce6e678a0ef3fba0", "ed8366874c0adad69486643d9dad9ec74e8750519c8bc56971a63517ec421d21",
		"linux", "s390x", "4aff31dc3e75fb4e46d667c3a9b4fc6ce87e57221ebbd484b8aff223e0c6c406", "24aed62d4ef97ce7cc8cb6075af3b19bf7acef6866f3bbd2eb5e32467af76317",
		"windows", "amd64", "24e19d6378782d2da343b80c632e342bcdd3bd1bf44e415a0ff81cf85ce1043a", "663d5d39c88013ef043a159f7ccc3bf6a17a1bb1346edfc2272e2b7e241daf27",
	)
}

// All returns every pinned tool, sorted by name.
func All() []Tool { return []Tool{Grype(), Syft()} }

// Lookup returns the pinned tool named name.
func Lookup(name string) (Tool, bool) {
	for _, t := range All() {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Archive returns t's archive for goos/goarch, if forge pins one.
func (t Tool) Archive(goos, goarch string) (Archive, bool) {
	for _, a := range t.Archives {
		if a.GOOS == goos && a.GOARCH == goarch {
			return a, true
		}
	}
	return Archive{}, false
}

// SHA256 returns the sha256 of t's archive for goos/goarch.
func (t Tool) SHA256(goos, goarch string) (string, bool) {
	a, ok := t.Archive(goos, goarch)
	return a.SHA256, ok
}

// BinarySHA256 returns the sha256 of the executable inside t's archive for
// goos/goarch.
func (t Tool) BinarySHA256(goos, goarch string) (string, bool) {
	a, ok := t.Archive(goos, goarch)
	return a.BinarySHA256, ok
}

// ReleaseURL returns where t's archive for goos/goarch is published.
func (t Tool) ReleaseURL(goos, goarch string) (string, bool) {
	return t.ReleaseURLFrom(DefaultBaseURL, goos, goarch)
}

// ReleaseURLFrom is ReleaseURL under another base: a mirror of github.com
// that serves the same paths.
func (t Tool) ReleaseURLFrom(base, goos, goarch string) (string, bool) {
	a, ok := t.Archive(goos, goarch)
	if !ok {
		return "", false
	}
	return strings.NewReplacer(
		"{base}", strings.TrimRight(base, "/"),
		"{repo}", t.Repo,
		"{version}", t.Version,
		"{archive}", a.Name,
	).Replace(ReleaseURLPattern), true
}

// Binary returns the executable's file name on goos.
func (t Tool) Binary(goos string) string { return binaryName(t.Name, goos) }

// ArchiveName returns the name goreleaser gives t's archive for goos/goarch,
// <name>_<version without v>_<goos>_<goarch>.<tar.gz|zip>, whether or not
// forge pins one.
func (t Tool) ArchiveName(goos, goarch string) string {
	return t.Name + "_" + strings.TrimPrefix(t.Version, "v") + "_" + goos + "_" + goarch + "." + formatFor(goos)
}

func formatFor(goos string) string {
	if goos == "windows" {
		return FormatZip
	}
	return FormatTarGz
}

func binaryName(name, goos string) string {
	if goos == "windows" {
		return name + ".exe"
	}
	return name
}

// tool builds a Tool from (goos, goarch, archive sha256, executable sha256)
// quadruples, already sorted.
func tool(name, version, repo, goPackage string, quads ...string) Tool {
	t := Tool{Name: name, Version: version, Repo: repo, GoPackage: goPackage}
	for i := 0; i+3 < len(quads); i += 4 {
		goos, goarch := quads[i], quads[i+1]
		t.Archives = append(t.Archives, Archive{
			GOOS: goos, GOARCH: goarch,
			Name:         t.ArchiveName(goos, goarch),
			Format:       formatFor(goos),
			SHA256:       quads[i+2],
			BinarySHA256: quads[i+3],
			Binary:       binaryName(name, goos),
		})
	}
	return t
}
