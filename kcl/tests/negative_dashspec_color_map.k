"""
Negative test — a Display.colors value must be a palette key: the enum check
reaches the values of a map, not only scalar fields.
"""

# expect: Display.colors values must each be one of
# expect: "danger"

import forge.dashspec as ds

_display = ds.Display {colors = {"rps" = "#ff0000"}}
