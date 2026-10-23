"""
Closed schema — forge.OnBucket rejects a field it does not declare.

Who may read the bucket is the bucket owner's policy, not something a deploy
declaration asserts. A field that does not exist needs no allowlist, and KCL
refuses it for free naming the schema. This fails if someone later adds a
passthrough that makes arbitrary keys legal again.
"""

# expect: public_access
# expect: OnBucket

import forge

_fe = forge.Frontend {
    name = "web"
    path = "web"
    runtime = forge.OnBucket {bucket = "gs://b", public_access = True}
}
