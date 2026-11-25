# expect: BuildOutput.platform 'macos' is not an os/arch pair
import forge

bad = forge.ShellBuild {
    cmd = "./scripts/package.sh"
    outputs = [forge.BuildOutput {path = "dist/acme.zip", platform = "macos"}]
}
