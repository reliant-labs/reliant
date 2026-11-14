"""
Positive test — `Observability.analytics` (forge.ProjectAnalytics) declares
the project's Events funnels and counts. A forge.Step renders as its event
name in `steps` and its `where`/`within` in `step_filters`, a bare name as {};
a funnel of bare names renders no `step_filters` at all. The user-activity
rules render as declared.
"""

import forge


output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {
        analytics = forge.ProjectAnalytics {
            funnels = [
                forge.Funnel {
                    id = "forge-deploy", by = "org", window = "30d"
                    steps = [
                        forge.Step {event = "deploy_succeeded", where = {first_for_org = "true"}}
                        "claim_completed"
                        forge.Step {event = "deploy_succeeded", where = {first_for_org = "false"}, within = "14d"}
                    ]
                }
                forge.Funnel {id = "plain", by = "person", window = "7d", steps = ["a_done", "b_done"]}
            ]
            counts = [forge.Count {id = "deploys", event = "deploy_succeeded", group_by = ["mode", "first_for_org"]}]
            not_activity = ["billing_subscription_state"]
            exclude_users_with = ["internal_user"]
        }
    }
})

_a = output.observability.analytics
_f = _a.funnels[0]
assert_steps_are_names = _f.steps == ["deploy_succeeded", "claim_completed", "deploy_succeeded"]
assert_step_filters = _f.step_filters == [{"where": {"first_for_org": "true"}}, {}, {"where": {"first_for_org": "false"}, "within": "14d"}]
assert_by_org = _f.by == "org" and _f.window == "30d"
assert_plain_has_no_filters = "step_filters" not in _a.funnels[1]
assert_activity_rules = _a.not_activity == ["billing_subscription_state"] and _a.exclude_users_with == ["internal_user"]
assert_count = _a.counts[0].event == "deploy_succeeded" and _a.counts[0].group_by == ["mode", "first_for_org"]
