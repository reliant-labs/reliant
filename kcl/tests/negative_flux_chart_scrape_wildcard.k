"""
Negative test — a wildcard scrape namespace is refused (the chart's own allow-scraping admits every namespace; naming `*` would restore it).
"""

# expect: scrape_namespaces entries

import forge

_c = forge.flux_chart(scrape_namespaces = ["*"])
