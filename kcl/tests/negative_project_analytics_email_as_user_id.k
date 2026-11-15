"""
Negative test — ProjectAnalytics.exclude_user_ids names users by the opaque id
their events carry, so an email (which an event never carries) is refused.
"""

# expect: ProjectAnalytics.exclude_user_ids are opaque user ids

import forge

_a = forge.ProjectAnalytics {exclude_user_ids = ["staff@example.com"]}
