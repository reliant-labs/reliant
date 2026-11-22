import forge

bundle = forge.Bundle {
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {mode = "enforce"}
        approvals = forge.Approvals {mode = "enforce"}
    }
}
