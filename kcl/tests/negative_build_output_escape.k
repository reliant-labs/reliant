# expect: BuildOutput.path 'dist/../../acme.tar.gz' leaves the build's working directory
import forge

bad = forge.ShellBuild {
    cmd = "./scripts/package.sh"
    outputs = [forge.BuildOutput {path = "dist/../../acme.tar.gz"}]
}
