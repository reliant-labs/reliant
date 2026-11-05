"""
Env-gated positive test: RenderedSecrets from='literal' is allowed in
dev/e2e, and refused under prod (the kcl-args / reject-args directives
below). A literal inline value is fine for non-sensitive fixed test values
where a dotenv round-trip is friction. A render with no env binding also
REJECTS the literal (see negative_rendered_secrets_literal_prod.k).
"""

# kcl-args: env=dev | env=e2e
# reject-args: env=prod
# reject-expect: RenderedSecretKey(from='literal') is only allowed in dev/e2e

import forge

_bundle = forge.Bundle {
    project = "fixture"
    workloads = []
    secret_provider = forge.RenderedSecrets {
        secrets = [
            forge.RenderedSecret {
                name = "test-fixtures"
                keys = {
                    "ISSUER" = forge.RenderedSecretKey {
                        from = "literal"
                        value = "https://test.local/"
                    }
                }
            }
        ]
    }
}

output = forge.render(_bundle)

# The literal value projects (dev/e2e only — gated by the schema check
# which passed because `-D env` is dev/e2e).
assert_literal_value = output.secret_provider.secrets[0].keys.ISSUER.value == "https://test.local/"
assert_literal_from = output.secret_provider.secrets[0].keys.ISSUER.from == "literal"
