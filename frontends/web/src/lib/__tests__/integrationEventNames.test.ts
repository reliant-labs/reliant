/**
 * Integration events read by their catalog names wherever a run or trigger
 * names one: "GitHub: Issue opened", not "github: issues.opened". The client
 * used to have no display names, so run headers, the canvas trigger rail and
 * the Automations list all printed the recorded ids.
 */

import { describe, expect, it } from 'vitest'
import { create } from '@bufbuild/protobuf'

import { IntegrationSourceSchema } from '@/gen/reliant/v1/trigger_pb'
import type { CatalogEntrySummary } from '@/api/catalog-search-grpc'
import { describeTriggerSource } from '../cronText'
import { describeDeclaredSource, type DeclaredTrigger } from '../declaredTriggers'
import { describeIntegrationEvents, integrationEventNaming, RAW_EVENT_NAMING } from '../integrationEventNames'
import { launchKindDisplay } from '../runStatus'
import { launchContextOf } from '../../components/runs/launchContext'

function trigger(integration: [string, string], displayName: string, events: string[]): Pick<CatalogEntrySummary, 'displayName' | 'events' | 'integration'> {
  return { displayName, events, integration: { id: integration[0], displayName: integration[1], version: 1, icon: '', category: '' } }
}

const naming = integrationEventNaming([
  trigger(['github', 'GitHub'], 'Issue opened', ['issues.opened']),
  trigger(['github', 'GitHub'], 'Issue closed', ['issues.closed']),
  trigger(['github', 'GitHub'], 'Push', ['push']),
  // One trigger type can cover several provider events.
  trigger(['slack', 'Slack'], 'New message', ['message.channels', 'message.groups', 'message.im']),
])

describe('integrationEventNaming', () => {
  it('names an integration and its events from the catalog trigger types', () => {
    expect(naming.integration('github')).toBe('GitHub')
    expect(naming.event('github', 'issues.opened')).toBe('Issue opened')
    expect(naming.event('slack', 'message.im')).toBe('New message')
  })

  it('reads anything the catalog does not know as recorded', () => {
    expect(naming.integration('jira')).toBe('jira')
    expect(naming.event('github', 'issues.*')).toBe('issues.*')
    // Event names are per integration.
    expect(naming.event('slack', 'push')).toBe('push')
  })

  it('describes a source the same way for declared triggers and automations', () => {
    expect(describeIntegrationEvents({ integration: 'github', events: ['issues.opened'] }, naming)).toBe('GitHub: Issue opened')
    expect(describeIntegrationEvents({ integration: 'github', events: ['issues.opened', 'issues.closed', 'push'] }, naming)).toBe(
      'GitHub: Issue opened, Issue closed +1',
    )
    expect(describeIntegrationEvents({ integration: 'github', events: [] }, naming)).toBe('GitHub')
    expect(describeIntegrationEvents({ integration: 'github', events: ['issues.opened'] }, RAW_EVENT_NAMING)).toBe('github: issues.opened')
  })
})

describe('run and trigger wording names integration events', () => {
  it('a run header: "Started by <automation> on GitHub: Issue opened"', () => {
    const context = launchContextOf({ kind: 'integration', occurredAt: '', manual: false, integration: 'github', providerEvent: 'issues.opened' }, naming)
    expect(launchKindDisplay('integration', { ...context, triggerName: 'Triage' }).startedByLine).toBe('Started by Triage on GitHub: Issue opened')
    expect(launchKindDisplay('integration', context).shortLabel).toBe('GitHub')
  })

  it('a declared trigger on the canvas and the detail page', () => {
    const declared = { name: 'on-issue', source: { case: 'integration', value: { integration: 'github', events: ['issues.opened'], match: {} } } } as unknown as DeclaredTrigger
    expect(describeDeclaredSource(declared, () => '', naming)).toBe('GitHub: Issue opened')
  })

  it('an automation in the Automations list', () => {
    const source = { kind: 'passthrough' as const, arm: { case: 'integration' as const, value: create(IntegrationSourceSchema, { integration: 'github', events: ['push'] }) } }
    expect(describeTriggerSource(source, naming)).toBe('GitHub: Push')
  })
})
