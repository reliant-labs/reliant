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
}

export const TRIGGER_CEL_NAMESPACE = 'trigger'

export const TRIGGER_CEL_FIELDS: readonly TriggerCelField[] = [
  {
    name: 'kind',
    type: 'string',
    description: 'What started the run: "chat.start", "schedule" or "agent.start_run".',
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
]

/** The CEL path for a field, e.g. `trigger.scheduled_for`. */
export function triggerCelPath(field: Pick<TriggerCelField, 'name'>): string {
  return `${TRIGGER_CEL_NAMESPACE}.${field.name}`
}
