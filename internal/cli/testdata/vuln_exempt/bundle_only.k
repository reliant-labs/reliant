import forge
import ..workloads as wl

bundle = forge.Bundle {
    project = "demo"
    env = "prod"
    workloads = [wl.api]
}

output = forge.render(bundle)
