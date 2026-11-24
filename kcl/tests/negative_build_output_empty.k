# expect: BuildOutput.path is required
import forge

bad = forge.GoBuild {
    cmd = "./cmd/acme"
    outputs = [forge.BuildOutput {path = "  "}]
}
