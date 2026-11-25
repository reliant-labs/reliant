// Package vulnpatch is the deterministic half of `forge vuln patch`: it bumps
// a vulnerable dependency to the lowest version that fixes it, with the
// ecosystem's own tool, verifies the result, and says per finding what
// happened.
//
// # The four steps
//
//  1. Resolve each finding to the manifests in the tree it comes from
//     ([Plan]). A Go module goes to every go.mod (go.work aware) that
//     requires it; an npm package to every package-lock.json,
//     pnpm-lock.yaml or yarn.lock that pins it at the version the finding
//     names, and only one package manager's; a PyPI package likewise to
//     every requirements file, poetry.lock or uv.lock that pins it at that
//     version, and only one tool's; an OS package (deb, apk, rpm) to the one
//     Dockerfile whose final stage could have built the image for the
//     scanned platform, with no build argument in the image of any FROM on
//     its way to the base (ospkg.go); the Go standard library to every
//     go.mod's toolchain directive and golang builder image (gostdlib.go). A
//     finding is bumped everywhere it is needed or nowhere: one the tree
//     cannot be tied to (no exact copy, manifests of several package
//     managers, a package.json or requirements file forge could not write
//     back faithfully, a Dockerfile or builder image it cannot move) is
//     skipped, with the reason, for the agent.
//  2. Choose the target ([ChooseTarget]): the lowest fixed version at or
//     above the installed one, in the installed major unless AllowMajor. A
//     major-only fix is skipped as "needs a major bump".
//  3. Apply it ([Apply]) with the ecosystem's own tool, so lockfiles stay
//     consistent: `go get mod@vX && go mod tidy`; `npm install pkg@X` or an
//     npm `overrides` entry; `pnpm update` or `pnpm.overrides`; `yarn up`
//     (`yarn upgrade` on yarn 1) or `resolutions`; a requirements pin
//     rewritten, or its pip-compile (or `uv pip compile`) command rerun with
//     --upgrade-package; `poetry update` or `poetry add pkg@^X`; `uv lock
//     --upgrade-package pkg==X` (Python versions are PEP 440, not semver);
//     `go mod edit -toolchain=`; a Dockerfile's FROM is rewritten in place,
//     to a tag and digest read from the image's registry ([Registry]).
//  4. Verify ([Verify]): run the project's gate, then rescan each changed
//     manifest's directory, and rebuild each changed Dockerfile and rescan
//     its image; a targeted finding that the rescan of one of its own
//     directories, or of one of its own rebuilt images, still reports, on
//     the same package, is still_present.
//
// Every command runs through a [vulnscan.Runner], so the tests drive the
// whole flow against fixture trees with a fake go, npm, pnpm, yarn,
// pip-compile, poetry, uv, docker, git and gh. The result is a [Document] (schema
// forge.dev/vuln-patch/v1), which the `vuln-patch` Reliant workflow reads to
// decide whether an agent takes over.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: every outbound effect (go, npm, pnpm, yarn, pip-compile, poetry, uv, docker, git, gh, the verify command) goes through the injectable vulnscan.Runner field the tests fake, and every registry read through the Registry option; a contract.go Service would be one implementation with no substitution point
//forge:exclude-contract: a CLI-driven dependency patcher, not an injected application component
package vulnpatch
