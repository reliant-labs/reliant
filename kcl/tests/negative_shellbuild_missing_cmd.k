"""
Negative test — ShellBuild requires `cmd`.
"""

# expect: ShellBuild.cmd is required

import forge
import forge.workloads as fw

_w = fw.Workload {name = "x", build = forge.ShellBuild {}}
