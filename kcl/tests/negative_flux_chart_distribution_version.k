"""
Negative test — the Flux version is forge's, not a consumer's: the flags forge writes are verified against FLUX_VERSION's minor, so `distribution` may name a mirror but never a version.
"""
# expect: the Flux version is forge's
import forge
_c = forge.flux_chart(distribution = {version = "2.8.x"})
