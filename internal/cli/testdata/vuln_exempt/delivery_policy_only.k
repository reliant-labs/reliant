import forge

bundle = forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        approvals = forge.Approvals {mode = "enforce"}
    }
}
