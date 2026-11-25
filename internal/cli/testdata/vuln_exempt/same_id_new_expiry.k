import forge

bundle = forge.Bundle {
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {
            mode = "enforce"
            exemptions = [
                forge.VulnExemption {id = "CVE-2026-1234", reason = "old reason", expires = "2026-11-30"}
                forge.VulnExemption {id = "GO-2026-0001", reason = "unused", expires = "2026-12-01"}
            ]
        }
    }
}
