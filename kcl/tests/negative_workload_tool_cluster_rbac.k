"""
Negative test — a `tool` cannot declare clusterRBAC.

clusterRBAC is allowed on every SCHEDULED kind. A tool is never scheduled:
it is built into the image and runs only when someone execs it, so there is
no pod and no ServiceAccount for a ClusterRoleBinding to name. Accepting the
declaration would render nothing and report success.
"""

# expect: clusterRBAC is not supported for kind='tool'

import forge.workloads as fw

_bad = fw.Workload {
    name = "admin-cli"
    kind = "tool"
    args = ["admin"]
    clusterRBAC = [{apiGroups = ["reliant.dev"], resources = ["workspaces"], verbs = ["list"]}]
}
