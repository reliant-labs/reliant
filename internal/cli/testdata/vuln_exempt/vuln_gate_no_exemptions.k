import forge

bundle = forge.Bundle {
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {
            mode = "enforce"
            block_severity = "high"
        }
    }
}
