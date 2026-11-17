"""
Negative test — a forge.Step with `where` belongs to a funnel counted over
product_events; a session funnel is a frontend dashboard panel of bare names.
"""

# expect: a forge.Step with `where` or `within` needs by

import forge

_f = forge.Funnel {
    id = "bad"
    steps = [forge.Step {event = "a_done", where = {k = "v"}}, "b_done"]
}
