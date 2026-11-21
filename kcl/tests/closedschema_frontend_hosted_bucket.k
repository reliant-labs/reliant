"""
Closed schema — forge.OnHosted has no `bucket`.

This is the point of the runtime split. "A StaticSite with no bucket is
hosted" was a hidden mode; a hosted frontend that NAMES a bucket was a
render-time check that had to remember to refuse it. Now the hosted runtime
simply has no such field, and KCL refuses it at compile time naming the
schema. For your own bucket, bind forge.OnBucket.

Not a negative_* fixture: the refusal is a CompileError, which that harness
deliberately does not accept (TestKCLModule_ClosedSchemas).
"""

# expect: bucket
# expect: OnHosted

import forge

_fe = forge.Frontend {
    name = "web"
    image = "ghcr.io/acme/web"
    path = "web"
    runtime = forge.OnHosted {bucket = "gs://mine"}
}
