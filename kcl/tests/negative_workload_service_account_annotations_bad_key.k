"""
NEGATIVE: fw.Workload validates `serviceAccountAnnotations` keys — an empty
prefix (`/name`) is not a legal annotation key, and the apiserver would
reject the ServiceAccount at apply time after everything else had rolled.
"""

# expect: invalid annotation key(s) [/gcp-service-account]

import forge.workloads as fw

_w = fw.Workload {
    name = "api"
    serviceAccountAnnotations = {"/gcp-service-account" = "api@p.iam.gserviceaccount.com"}
}
