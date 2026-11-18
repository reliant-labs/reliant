"""
Negative test — an impersonation target that is not a ServiceAccount name is refused, so the resourceNames pin can never be a malformed string.
"""

# expect: impersonate_service_account

import forge

_c = forge.flux_chart(impersonate_service_account = "Reliant Deploy")
