"""
Negative test — a template reading an undeclared data key is refused, naming it and the declared keys.
"""

# expect: templates read data keys that `data` does not declare
# expect: missing_key

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "app", keys = ["password", "url"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"password" = "P"}
            templates = {"url" = "pg://{{ .missing_key }}@h"}
        }
    }]
})
