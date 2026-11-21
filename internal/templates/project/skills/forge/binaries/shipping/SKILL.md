---
name: binaries/shipping
description: Binaries you ship to users rather than run — a CLI download, an agent — built once per OS/arch from one GoBuild with `forge.BuildOnly {build_variants = [...]}` (per-variant goos/goarch, ldflags, tags, build-time env such as CGO_ENABLED, output_name), and how each binary is recorded in the release.
---

# Shipping binaries per platform: `build_variants`

A binary you ship to users (a CLI download, an agent) rather than run is a
`forge.BuildOnly` workload. Its `build_variants` builds the GoBuild `cmd` once
per variant into `bin/<output_name>` (`.exe` added on windows), and a
`forge env build <env> --release <v>` records each binary's sha256 in the
release:

```kcl
cli = fw.Workload {
    name = "acme-cli"
    kind = "tool"
    build = forge.GoBuild {cmd = "./cmd/acme"}
    runtime = forge.BuildOnly {build_variants = [
        forge.BuildVariant {name = "linux-amd64", goos = "linux", goarch = "amd64", ldflags = ["-s", "-w"]}
        forge.BuildVariant {name = "windows-amd64", goos = "windows", goarch = "amd64"}
        forge.BuildVariant {
            name = "darwin-arm64", goos = "darwin", goarch = "arm64"
            env = {"CGO_ENABLED" = "1"}          # variants build CGO_ENABLED=0 unless env says otherwise
            output_name = "acme_Darwin_arm64"    # default: <workload>-<variant>
        }
    ]}
}
```

`env` is that variant's build-time process env, merged over forge's (declared
keys win); `output_name` is the file name, unique across the workload's
variants. Archives, checksums files and installers are still your packaging
step — a `forge.ShellBuild` that runs it.
