"""
Negative test — a `tool` cannot declare RBAC.

A tool is built into the image and never scheduled, so it renders no
workload manifest at all. There is no pod, and therefore no ServiceAccount
for a Role to bind to. Silently accepting the declaration would render
nothing and report success, leaving the author believing a permission had
been granted to something that does not run.
"""

# expect: namespacedRBAC is not supported for kind='tool'

import forge.workloads as fw

_bad = fw.Workload {
    name = "admin-cli"
    kind = "tool"
    args = ["admin"]
    namespacedRBAC = [
        {apiGroups = [""], resources = ["configmaps"], verbs = ["get"]}
    ]
}
