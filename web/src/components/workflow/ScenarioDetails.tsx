/**
 * ScenarioDetails - Structured detail view + YAML viewer for scenario tests
 * 
 * Shows:
 * - EventSequence: timeline of simulated events
 * - ExpectationsView: expected outcomes as a checklist
 * - InputsView: formatted JSON inputs
 * - YamlView: raw YAML with copy button
 */

import { useState, useEffect } from 'react'
import {
  AlertTriangle,
  Target,
  CheckCircle,
  XCircle,
  Copy,
  Check,
  Code,
  List,
  ChevronDown,
  ChevronRight,
  RefreshCw,
  Zap,
} from 'lucide-react'
import { cn } from '../../lib/utils'
import { exportScenario, type Scenario } from '../../api/scenarios'
import type { SimulatedEvent, ScenarioExpectation } from '../../gen/reliant/v1/workflow_pb'

// ─── EventSequence ──────────────────────────────────────────────────────────

function truncate(text: string, maxLen: number): string {
  if (text.length <= maxLen) return text
  return text.slice(0, maxLen) + '…'
}

// Format output JSON for display - shows key fields or truncated JSON
function formatOutputPreview(outputJson: string): string {
  try {
    const output = JSON.parse(outputJson)
    // Show key fields based on common activity outputs
    if (output.response_text) {
      return truncate(output.response_text, 80)
    }
    if (output.stdout !== undefined || output.exit_code !== undefined) {
      return `exit: ${output.exit_code ?? 0}${output.stdout ? `, stdout: ${truncate(output.stdout, 50)}` : ''}`
    }
    if (output.status) {
      return output.status
    }
    // Fallback: show truncated JSON
    return truncate(outputJson, 80)
  } catch {
    return truncate(outputJson, 80)
  }
}

function EventData({ event }: { event: SimulatedEvent }) {
  if (!event.outputJson) {
    return <span className="text-muted-foreground italic">no output defined</span>
  }
  return (
    <span className="text-foreground/80">
      {formatOutputPreview(event.outputJson)}
    </span>
  )
}

export function EventSequence({ events }: { events: SimulatedEvent[] }) {
  if (!events || events.length === 0) {
    return (
      <div className="text-xs text-muted-foreground italic py-1">
        No events defined
      </div>
    )
  }

  return (
    <div className="space-y-0">
      {events.map((event, idx) => (
        <div key={idx} className="flex items-start gap-2 relative">
          {/* Timeline connector */}
          {idx < events.length - 1 && (
            <div className="absolute left-[7px] top-[18px] bottom-0 w-px bg-border" />
          )}
          {/* Icon */}
          <div className="flex-shrink-0 mt-0.5 z-10 bg-background">
            <Zap className="w-3.5 h-3.5 text-purple-500" />
          </div>
          {/* Content */}
          <div className="flex-1 min-w-0 pb-2.5">
            <div className="flex items-center gap-1.5">
              {event.node ? (
                <span className="text-2xs px-1 py-0.5 rounded bg-muted font-mono text-muted-foreground">
                  {event.node}
                </span>
              ) : (
                <span className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
                  Event
                </span>
              )}
              <span className="text-2xs text-muted-foreground/50">#{idx + 1}</span>
            </div>
            <div className="text-xs mt-0.5 break-all">
              <EventData event={event} />
            </div>
          </div>
        </div>
      ))}
    </div>
  )
}

// ─── ExpectationsView ───────────────────────────────────────────────────────

export function ExpectationsView({ expect }: { expect: ScenarioExpectation | undefined }) {
  if (!expect) {
    return (
      <div className="text-xs text-muted-foreground italic">
        No expectations defined
      </div>
    )
  }

  const hasContent = expect.outcome || (expect.reached && expect.reached.length > 0) || 
    (expect.notReached && expect.notReached.length > 0) || expect.errorContains || expect.errorNode

  if (!hasContent) {
    return (
      <div className="text-xs text-muted-foreground italic">
        No expectations defined
      </div>
    )
  }

  return (
    <div className="space-y-1.5">
      {expect.outcome && (
        <div className="flex items-center gap-2 text-xs">
          <Target className="w-3 h-3 text-muted-foreground" />
          <span className="text-muted-foreground">Outcome:</span>
          <span className={cn(
            "font-mono font-medium",
            expect.outcome === 'completed' ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'
          )}>
            {expect.outcome}
          </span>
        </div>
      )}

      {expect.reached && expect.reached.length > 0 && (
        <div className="flex items-start gap-2 text-xs">
          <CheckCircle className="w-3 h-3 text-emerald-500 mt-0.5" />
          <div>
            <span className="text-muted-foreground">Should reach: </span>
            {expect.reached.map((node, i) => (
              <span key={node}>
                {i > 0 && <span className="text-muted-foreground">, </span>}
                <span className="font-mono text-emerald-600 dark:text-emerald-400">{node}</span>
              </span>
            ))}
          </div>
        </div>
      )}

      {expect.notReached && expect.notReached.length > 0 && (
        <div className="flex items-start gap-2 text-xs">
          <XCircle className="w-3 h-3 text-red-500 mt-0.5" />
          <div>
            <span className="text-muted-foreground">Should NOT reach: </span>
            {expect.notReached.map((node, i) => (
              <span key={node}>
                {i > 0 && <span className="text-muted-foreground">, </span>}
                <span className="font-mono text-red-600 dark:text-red-400">{node}</span>
              </span>
            ))}
          </div>
        </div>
      )}

      {expect.errorContains && (
        <div className="flex items-center gap-2 text-xs">
          <AlertTriangle className="w-3 h-3 text-amber-500" />
          <span className="text-muted-foreground">Error contains:</span>
          <span className="font-mono text-amber-600 dark:text-amber-400">
            "{expect.errorContains}"
          </span>
        </div>
      )}

      {expect.errorNode && (
        <div className="flex items-center gap-2 text-xs">
          <AlertTriangle className="w-3 h-3 text-amber-500" />
          <span className="text-muted-foreground">Error at node:</span>
          <span className="font-mono text-amber-600 dark:text-amber-400">{expect.errorNode}</span>
        </div>
      )}
    </div>
  )
}

// ─── InputsView ─────────────────────────────────────────────────────────────

export function InputsView({ inputsJson }: { inputsJson: string | undefined }) {
  // Hooks must be called before any conditional returns
  // Calculate formatted value for hook initialization
  let formatted: string = '';
  if (inputsJson) {
    try {
      formatted = JSON.stringify(JSON.parse(inputsJson), null, 2);
    } catch {
      formatted = inputsJson;
    }
  }
  const [isExpanded, setIsExpanded] = useState(formatted ? formatted.split('\n').length > 5 : false);

  if (!inputsJson) return null

  // For short JSON, always show it
  if (formatted.split('\n').length <= 5) {
    return (
      <div>
        <div className="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-1">
          Inputs
        </div>
        <pre className="text-xs font-mono bg-muted/50 rounded px-2 py-1.5 overflow-x-auto whitespace-pre">
          {formatted}
        </pre>
      </div>
    )
  }

  return (
    <div>
      <button
        onClick={() => setIsExpanded(!isExpanded)}
        className="flex items-center gap-1 text-xs font-medium text-muted-foreground uppercase tracking-wide mb-1 hover:text-foreground transition-colors"
      >
        {isExpanded ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
        Inputs
      </button>
      {isExpanded && (
        <pre className="text-xs font-mono bg-muted/50 rounded px-2 py-1.5 overflow-x-auto whitespace-pre max-h-48 overflow-y-auto">
          {formatted}
        </pre>
      )}
    </div>
  )
}

// ─── ScenarioYamlView ───────────────────────────────────────────────────────

export function ScenarioYamlView({ scenario, projectId }: { scenario: Scenario; projectId: string }) {
  const [yaml, setYaml] = useState<string | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    let cancelled = false

    async function fetchYaml() {
      setIsLoading(true)
      setError(null)
      try {
        const result = await exportScenario({ projectId, scenarioId: scenario.id })
        if (!cancelled) {
          setYaml(result.yamlContent)
        }
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : 'Failed to load YAML')
        }
      } finally {
        if (!cancelled) {
          setIsLoading(false)
        }
      }
    }

    fetchYaml()
    return () => { cancelled = true }
  }, [projectId, scenario.id])

  const handleCopy = async () => {
    if (!yaml) return
    try {
      await navigator.clipboard.writeText(yaml)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // fallback
    }
  }

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 text-xs text-muted-foreground py-4 justify-center">
        <RefreshCw className="w-3 h-3 animate-spin" />
        Loading YAML...
      </div>
    )
  }

  if (error) {
    return (
      <div className="text-xs text-red-500 py-2">
        {error}
      </div>
    )
  }

  if (!yaml) return null

  return (
    <div className="relative">
      <button
        onClick={handleCopy}
        className="absolute top-2 right-2 p-1 rounded hover:bg-muted transition-colors z-10"
        title="Copy YAML"
      >
        {copied ? (
          <Check className="w-3.5 h-3.5 text-emerald-500" />
        ) : (
          <Copy className="w-3.5 h-3.5 text-muted-foreground" />
        )}
      </button>
      <pre className="text-xs font-mono bg-muted/50 rounded px-3 py-2 overflow-x-auto whitespace-pre max-h-96 overflow-y-auto">
        {yaml}
      </pre>
    </div>
  )
}

// ─── ScenarioDetailView (combined) ─────────────────────────────────────────

type DetailTab = 'details' | 'yaml'

export function ScenarioDetailView({ scenario, projectId }: { scenario: Scenario; projectId: string }) {
  const [activeTab, setActiveTab] = useState<DetailTab>('details')

  return (
    <div className="space-y-3">
      {/* Tab switcher */}
      <div className="flex items-center gap-1 border-b border-border">
        <button
          onClick={() => setActiveTab('details')}
          className={cn(
            "flex items-center gap-1.5 px-2 py-1.5 text-xs font-medium transition-colors border-b-2 -mb-px",
            activeTab === 'details'
              ? "border-foreground text-foreground"
              : "border-transparent text-muted-foreground hover:text-foreground"
          )}
        >
          <List className="w-3 h-3" />
          Details
        </button>
        <button
          onClick={() => setActiveTab('yaml')}
          className={cn(
            "flex items-center gap-1.5 px-2 py-1.5 text-xs font-medium transition-colors border-b-2 -mb-px",
            activeTab === 'yaml'
              ? "border-foreground text-foreground"
              : "border-transparent text-muted-foreground hover:text-foreground"
          )}
        >
          <Code className="w-3 h-3" />
          YAML
        </button>
      </div>

      {/* Tab content */}
      {activeTab === 'details' ? (
        <div className="space-y-4">
          {/* Events */}
          <div>
            <div className="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-2">
              Events ({scenario.events?.length ?? 0})
            </div>
            <EventSequence events={scenario.events ?? []} />
          </div>

          {/* Expectations */}
          {scenario.expect && (
            <div>
              <div className="text-xs font-medium text-muted-foreground uppercase tracking-wide mb-1.5">
                Expectations
              </div>
              <ExpectationsView expect={scenario.expect} />
            </div>
          )}

          {/* Inputs - check the scenario for inputsJson from definition context */}
          {(scenario as unknown as { inputsJson?: string }).inputsJson && (
            <InputsView inputsJson={(scenario as unknown as { inputsJson?: string }).inputsJson} />
          )}
        </div>
      ) : (
        <ScenarioYamlView scenario={scenario} projectId={projectId} />
      )}
    </div>
  )
}
