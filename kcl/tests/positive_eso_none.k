"""
Positive test — a Bundle with no ExternalSecretsOperator renders no chart and no
component record: the component is opt-in and unset renders nothing.
"""

import forge

output = forge.render(forge.Bundle {project = "fixture", env = "dev"})

assert_no_chart = len(output.helm_charts) == 0
assert_no_record = output.external_secrets_operators == []
