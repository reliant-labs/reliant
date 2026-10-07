import { describe, expect, it } from 'vitest'

import {
  defaultSource,
  findingsForTrigger,
  integrationOf,
  newDeclaredTrigger,
  scheduleOf,
  slugifyTriggerName,
  triggerNameError,
  uniqueTriggerName,
  withFilter,
  withInput,
  withIntegration,
  withSchedule,
  type DeclaredTrigger,
} from '../declaredTriggers'

const issue = newDeclaredTrigger({
  name: 'new-issue',
  source: { case: 'integration', value: { integration: 'github', events: ['issues.opened'], match: { repository: 'acme/app' } } } as DeclaredTrigger['source'],
})

describe('declared triggers', () => {
  it('edits source, filter and inputs as the proto shape', () => {
    let t = withFilter(issue, "trigger.payload.data.issue.user.login != 'bot'")
    t = withInput(t, 'issue_number', '{{ trigger.payload.data.issue.number }}')
    t = withIntegration(t, { ...integrationOf(t)!, events: ['issues.opened', 'issues.labeled'], match: { repository: 'acme/app', '': 'dropped' } })
    expect(t).toMatchObject({
      name: 'new-issue',
      filter: "trigger.payload.data.issue.user.login != 'bot'",
      inputs: { issue_number: '{{ trigger.payload.data.issue.number }}' },
      source: { case: 'integration', value: { integration: 'github', events: ['issues.opened', 'issues.labeled'], match: { repository: 'acme/app' } } },
    })
    expect(withInput(t, 'issue_number', '').inputs).toEqual({})
  })

  it('round-trips a schedule and drops blank cron lines', () => {
    const t = withSchedule(newDeclaredTrigger({ name: 'nightly', source: defaultSource('schedule') }), { cron: ['0 2 * * *', ' '], timezone: 'UTC' })
    expect(scheduleOf(t)).toEqual({ cron: ['0 2 * * *'], interval: undefined, timezone: 'UTC' })
  })

  it('names triggers as the server requires', () => {
    expect(slugifyTriggerName('New issue opened!')).toBe('new-issue-opened')
    expect(uniqueTriggerName('new issue', [issue])).toBe('new-issue-2')
    expect(triggerNameError('New Issue', [issue], 1)).toMatch(/lowercase/)
    expect(triggerNameError('new-issue', [issue, issue], 1)).toMatch(/already/)
    expect(triggerNameError('new-issue', [issue], 0)).toBeUndefined()
  })

  it('maps server findings onto the trigger they name, by index and name', () => {
    const errors = [
      { message: 'triage.triggers[0](nightly).schedule.cron: "0 25 * * *" is not a valid cron expression' },
      { message: 'triage.triggers[1](new-issue).filter: undeclared reference to \'trigger.foo\' (guard optional fields with has())', suggestion: 'guard optional fields with has()' },
      { message: 'triage.triggers[1](renamed-since).inputs.x: "x" is not a declared input' },
      { message: 'triage.nodes[0].uses: unknown action' },
    ]
    expect(findingsForTrigger(errors, 0, 'nightly')).toEqual([
      { field: 'schedule.cron', message: '"0 25 * * *" is not a valid cron expression', suggestion: undefined },
    ])
    expect(findingsForTrigger(errors, 1, 'new-issue')).toEqual([
      { field: 'filter', message: "undeclared reference to 'trigger.foo'", suggestion: 'guard optional fields with has()' },
    ])
  })
})
