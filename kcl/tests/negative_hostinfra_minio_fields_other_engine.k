"""
Negative test — the minio_* fields describe engine 'minio' only.
"""

# expect: are for engine 'minio' only

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    infra = [forge.HostInfra {name = "nats", engine = "nats", port = 4222, minio_console_port = 9201}]
})
