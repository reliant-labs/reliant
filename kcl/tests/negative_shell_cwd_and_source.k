# expect: ShellBuild.cwd and source are mutually exclusive
import forge
bad = forge.ShellBuild {
    cmd = "make build"
    cwd = "../lib"
    source = forge.GitSource {repo = "github.com/acme/lib", ref = "v1"}
}
