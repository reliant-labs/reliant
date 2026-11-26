# expect: ShellBuild.inputs entries are globs relative to the build's cwd
import forge

bad = forge.ShellBuild {
    cmd = "make image"
    inputs = ["src/**", "!"]
}
