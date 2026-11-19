"""
Negative test — a ResourceSet default identity that is not a ServiceAccount name is refused.
"""
# expect: resourceset_service_account
import forge
_c = forge.flux_chart(resourceset_service_account = "Flux ResourceSet")
