"""A KubernetesInventory interval that is not a collector duration is refused."""
# expect: KubernetesInventory.node_interval 'every minute' must be a duration like 60s
import forge

_inventory = forge.KubernetesInventory {node_interval = "every minute"}
