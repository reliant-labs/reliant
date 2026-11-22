"""
Negative test — ProjectAnalytics' user-activity rules name product events, so
a value that is not an event name is refused.
"""

# expect: ProjectAnalytics.not_activity and .exclude_users_with must be product event names

import forge

_a = forge.ProjectAnalytics {exclude_users_with = ["internal user'"]}
