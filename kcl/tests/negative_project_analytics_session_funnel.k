"""
Negative test — ProjectAnalytics.funnels are drawn on the Product dashboard,
so a session funnel there is refused.
"""

# expect: ProjectAnalytics.funnels are drawn on the Product dashboard

import forge

_a = forge.ProjectAnalytics {funnels = [forge.Funnel {id = "s", steps = ["a_done", "b_done"]}]}
