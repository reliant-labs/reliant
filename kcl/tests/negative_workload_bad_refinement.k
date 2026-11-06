"""
Negative test — an environment cannot refine a VALID base workload into
an INVALID one.

This is the other half of positive_workload_inheritance.k, and the
reason a base declaration is a schema instance rather than a plain
value: `|` preserves the schema type, so the Workload `check:` rules
re-run on the MERGED result. The error surfaces in the environment that
made the bad override, naming the field.
"""

# expect: replicas

import forge.workloads as fw

# A complete, VALID base declaration — exactly what workloads.k states.
api = fw.Workload {
    name = "api"
    kind = "service"
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
}

# The environment refines it into something invalid.
_prod = api | {replicas = 0}
