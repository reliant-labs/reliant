# expect: ShellBuild.built_images keys are the environment variable names
import forge

bad = forge.ShellBuild {
    cmd = "make image"
    built_images = {"BASE-RUNTIME" = "runtime"}
}
