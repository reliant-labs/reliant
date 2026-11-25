# expect: BuildOutput.path '/tmp/acme.tar.gz' is absolute
import forge

bad = forge.ShellBuild {
    cmd = "./scripts/package.sh"
    outputs = [forge.BuildOutput {path = "/tmp/acme.tar.gz"}]
}
