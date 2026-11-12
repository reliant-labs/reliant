"""
Negative test — a scrape namespace that is not a namespace name is refused.
"""

# expect: scrape_namespaces entries

import forge

_c = forge.flux_chart(scrape_namespaces = ["Not A Namespace"])
