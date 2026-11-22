# expect: DockerBuild.inputs entries are relative to the project root
import forge

bad = forge.DockerBuild {
    inputs = ["src/**", "!/etc/hosts"]
}
