# expect: ShellBuild.built_images and env both set the same variable
import forge

bad = forge.ShellBuild {
    cmd = "make image"
    env = {BASE = "ghcr.io/acme/runtime:latest"}
    built_images = {BASE = "runtime"}
}
