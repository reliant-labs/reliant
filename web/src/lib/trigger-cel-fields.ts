/**
 * The `trigger` CEL namespace: the event that started a run, fixed at launch.
 *
 * Mirrors `TriggerInfo.CELValue` in internal/workflow/runtime/trigger_info.go.
 * Every key is always present (an empty string when it does not apply), so an
 * expression never fails with "no such key". The backend catalog lists
 * `trigger` as a dynamic namespace without fields, which is why the field list
 * lives here: the builder's Trigger payload panel and Monaco completion both
 * read it.
 *
 * When integration triggers land, a manifest's `payload_schema` adds keys
 * under `payload` (research/WORKFLOW_UI.md §3.4 seam 3).
 */

export interface TriggerCelField {
  /** The key under `trigger`. */
  name: string
  type: string
  description: string
  /** A map's own keys, always present, for `trigger.<name>.` completion. */
  fields?: readonly TriggerCelField[]
}

export const TRIGGER_CEL_NAMESPACE = 'trigger'

/**
 * Every value `trigger.kind` takes: core.TriggerEventKind in
 * internal/db/core/trigger.go, in its declaration order. A test reads that
 * file and fails when the two lists drift, so a new kind cannot ship without
 * the builder describing it.
 */
export const TRIGGER_KINDS = [
  'chat.start',
  'schedule',
  'agent.start_run',
  'builder.test',
  'webhook',
  'integration',
  'workflow_event',
] as const

/**
 * Every value `trigger.sender.kind` takes: core.TriggerSenderKind in
 * internal/db/core/trigger.go, in its declaration order (a test reads that
 * file). Empty for a run a person started themselves.
 */
export const TRIGGER_SENDER_KINDS = ['slack', 'github', 'email', 'sms', 'webhook', 'workflow', 'schedule', 'user'] as const

/** `"a", "b" or "c"`: the kinds as the description lists them. */
function quotedList(values: readonly string[]): string {
  const quoted = values.map((value) => `"${value}"`)
  return quoted.length <= 1 ? quoted.join('') : `${quoted.slice(0, -1).join(', ')} or ${quoted[quoted.length - 1]}`
}

export const TRIGGER_CEL_FIELDS: readonly TriggerCelField[] = [
  {
    name: 'kind',
    type: 'string',
    description: `What started the run: ${quotedList(TRIGGER_KINDS)}.`,
  },
  {
    name: 'name',
    type: 'string',
    description: "The automation's name. Empty when no automation started the run.",
  },
  {
    name: 'scheduled_for',
    type: 'string',
    description: 'When a scheduled run was due (RFC 3339). Otherwise, when the run started.',
  },
  {
    name: 'trigger_id',
    type: 'string',
    description: 'The id of the automation that fired. Empty for a chat.',
  },
  {
    name: 'event_id',
    type: 'string',
    description: 'The id of this firing, unique per run.',
  },
  {
    name: 'occurred_at',
    type: 'string',
    description: 'When the start was recorded (RFC 3339).',
  },
  {
    name: 'payload',
    type: 'map',
    description: 'Everything the source sent, by key, for example trigger.payload.manual on a "Run now".',
  },
  {
    name: 'sender',
    type: 'map',
    description:
      'Who sent the event, as the source authenticated it (never read from the payload). To run only for certain people: trigger.sender.verified && trigger.sender.id in ["U123"].',
    fields: [
      {
        name: 'kind',
        type: 'string',
        description: `What sent it: ${quotedList(TRIGGER_SENDER_KINDS)}. Empty for a run you started yourself.`,
      },
      {
        name: 'id',
        type: 'string',
        description:
          "The source's id for the sender: a Slack user id, a GitHub login or an email address (both lowercased), an SMS number, a webhook's trigger id, a workflow's name.",
      },
      { name: 'display_name', type: 'string', description: 'A name for people to read. Never decide on it.' },
      {
        name: 'verified',
        type: 'bool',
        description:
          'The source vouches for id: a signed Slack or GitHub delivery, an email that passed DMARC, a webhook caller holding the token. An SMS number never is.',
      },
    ],
  },
]

/** The CEL path for a field, e.g. `trigger.scheduled_for`. */
export function triggerCelPath(field: Pick<TriggerCelField, 'name'>): string {
  return `${TRIGGER_CEL_NAMESPACE}.${field.name}`
}
