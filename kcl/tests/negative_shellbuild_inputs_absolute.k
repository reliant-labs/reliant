# expect: ShellBuild.inputs entries are relative to the build's cwd
import forge

bad = forge.ShellBuild {
    cmd = "make image"
    inputs = ["src/**", "!/etc/hosts"]
}
