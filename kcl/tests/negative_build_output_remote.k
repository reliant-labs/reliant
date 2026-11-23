# expect: RemoteBuild.outputs: a remote build leaves nothing on this machine to hash
import forge

bad = forge.RemoteBuild {
    source = forge.GitSource {repo = "github.com/acme/api", ref = "v1.0.0"}
    outputs = [forge.BuildOutput {path = "dist/acme.zip"}]
}
