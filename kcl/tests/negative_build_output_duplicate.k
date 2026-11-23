# expect: Build.outputs names the same path twice
import forge

bad = forge.ShellBuild {
    cmd = "./scripts/package.sh"
    outputs = [forge.BuildOutput {path = "dist/acme.zip"}, forge.BuildOutput {path = "dist/acme.zip", platform = "linux/amd64"}]
}
