"""Frontend startup dependencies are refused, never silently discarded."""
# expect: frontend 'web' cannot run on the host runtime
# expect: startup_after
import forge
output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [forge.Frontend {name = "web", path = "web", runtime = forge.OnHost {startup_after = ["api"]}}]
})
