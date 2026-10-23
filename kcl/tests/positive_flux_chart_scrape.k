"""
Positive test — allow-scraping is opt-in by namespace, and the allow-egress
invariant holds however it is configured.

The chart's own allow-scraping admits :8080 from EVERY namespace. The default
here admits none; a consumer names the namespaces that scrape.
"""

import forge

_one = forge.flux_chart(scrape_namespaces = ["observability"]).values.extraObjects
_two = forge.flux_chart(scrape_namespaces = ["observability", "monitoring"]).values.extraObjects
_scrape = lambda objs: [any] -> any { [o for o in objs if o.kind == "NetworkPolicy" and o.metadata.name == "allow-scraping"] }
_egress = lambda objs: [any] -> any { [o for o in objs if o.kind == "NetworkPolicy" and o.metadata.name == "allow-egress"][0] }

assert_scrape_present = len(_scrape(_one)) == 1
assert_scrape_port_only = _scrape(_one)[0].spec.ingress[0].ports == [{port = 8080, protocol = "TCP"}]
assert_scrape_names_namespace = _scrape(_one)[0].spec.ingress[0]["from"][0].namespaceSelector.matchExpressions == [{key = "kubernetes.io/metadata.name", operator = "In", values = ["observability"]}]
assert_scrape_two = _scrape(_two)[0].spec.ingress[0]["from"][0].namespaceSelector.matchExpressions[0].values == ["observability", "monitoring"]
# Never an all-namespaces selector.
assert_scrape_not_all = "namespaceSelector: {}" not in str(_scrape(_one)) and _scrape(_one)[0].spec.ingress[0]["from"][0].namespaceSelector != {}
# No allow-webhooks, whatever is configured.
assert_no_webhooks = len([o for o in _one if o.kind == "NetworkPolicy" and o.metadata.name == "allow-webhooks"]) == 0
# The invariant is the same object in every configuration.
assert_egress_invariant_same = _egress(_one) == _egress(_two) and _egress(_one) == _egress(forge.flux_chart().values.extraObjects)
assert_egress_ingress = _egress(_one).spec.ingress == [{"from" = [{podSelector = {}}]}]
