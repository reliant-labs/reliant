/**
 * TriggerPayloadPanel — the start node's side panel (research/WORKFLOW_UI.md
 * §3.2). Its "Trigger payload" tab lists what `trigger.*` exposes to CEL;
 * clicking a field inserts the path into the CEL input focused last, through
 * CELCompletionContext's insertion registry.
 *
 * The builder docks this panel beside an open step panel instead of replacing
 * it, so the expression being written and the fields to put in it are on
 * screen together.
 */

import { useState } from 'react'
import { Braces, Rocket } from 'lucide-react'
import { ConfigurationPanel } from '../ConfigurationPanel'
import { ConfigPanelTabBar, type ConfigTab } from './ConfigPanelTabBar'
import { useCELInsertTarget } from '../CELCompletionContext'
import { TRIGGER_CEL_FIELDS, triggerCelPath } from '../../../lib/trigger-cel-fields'
import { cn } from '../../../lib/utils'

const TABS: ConfigTab[] = [{ id: 'payload', label: 'Trigger payload' }]

export interface TriggerPayloadPanelProps {
  onClose: () => void
  bottomOffset?: number
  topOffset?: number
  /** Dock left of an open step panel rather than over it. */
  docked?: boolean
}

export function TriggerPayloadPanel({ onClose, bottomOffset, topOffset, docked = false }: TriggerPayloadPanelProps) {
  const [activeTab, setActiveTab] = useState('payload')
  const target = useCELInsertTarget()

  return (
    <div className={cn('cpv2-trigger-payload', docked && 'cpv2-trigger-payload--docked')}>
      <ConfigurationPanel
        title="Workflow start"
        subtitle="How a run begins"
        subtitleMono={false}
        icon={<Rocket />}
        onClose={onClose}
        bottomOffset={bottomOffset}
        topOffset={topOffset}
        tabBar={<ConfigPanelTabBar tabs={TABS} activeTab={activeTab} onTabChange={setActiveTab} />}
      >
        <div className="cpv2-section">
          <p className="cpv2-field-hint">
            Every run carries the event that started it. Use these fields in any expression, for
            example to name a scheduled run after its time.
          </p>
          <p className="cpv2-trigger-payload-target" aria-live="polite">
            {target
              ? <>Inserts into <span className="font-medium text-foreground">{target.label || 'the last expression field'}</span></>
              : 'Click into an expression field in a step, then pick a field to insert it.'}
          </p>
        </div>

        <ul aria-label="Trigger fields" className="cpv2-trigger-payload-list">
          {TRIGGER_CEL_FIELDS.map((field) => {
            const path = triggerCelPath(field)
            return (
              <li key={field.name}>
                <button
                  type="button"
                  className="cpv2-trigger-payload-field"
                  disabled={!target}
                  onClick={() => target?.insert(path)}
                  title={target ? `Insert ${path}` : undefined}
                >
                  <span className="flex items-center gap-1.5">
                    <Braces className="h-3 w-3 flex-shrink-0 text-muted-foreground" aria-hidden />
                    <code className="cpv2-trigger-payload-path">{path}</code>
                    <span className="cpv2-trigger-payload-type">{field.type}</span>
                  </span>
                  <span className="cpv2-trigger-payload-desc">{field.description}</span>
                </button>
              </li>
            )
          })}
        </ul>
      </ConfigurationPanel>
    </div>
  )
}
