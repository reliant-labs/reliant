import forge as f

_policy = f.DeliveryPolicy {
    vuln_gate = f.VulnPolicy {mode = "enforce", exemptions = [f.VulnExemption {id = "GO-2026-0001", reason = "unused", expires = "2026-12-01"}]}
}
