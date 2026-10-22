// Copyright (c) 2025 Reliant Labs

/**
 * Read-only view of one preset, with "copy to a new preset" for built-ins.
 * Extracted unchanged from the retired WorkflowHub (WORKFLOW_UI.md §1.2,
 * decision 7); hosted by workflow detail's Presets section and Settings →
 * Presets.
 */

import { useState } from 'react'
import { Copy } from 'lucide-react'
import { toast } from 'sonner'
import { cn } from '../../../lib/utils'
import { Modal } from '../../ui/Modal'
import { Button } from '../../ui/Button'
import type { Preset } from '../../../store/globalDataStore'
import { presetGrpc } from '../../../api/preset-grpc'
import { formatValueForDisplay, unwrapProtoValue } from '../../../lib/paramUtils'

interface PresetViewModalProps {
  preset: Preset
  projectId: string
  onCopy: () => void
  onClose: () => void
}

export function PresetViewModal({ preset, projectId, onCopy, onClose }: PresetViewModalProps) {
  const [isCopying, setIsCopying] = useState(false)
  const [copyName, setCopyName] = useState(`my-${preset.name}`)

  const handleCopy = async () => {
    if (!copyName.trim()) return

    setIsCopying(true)
    try {
      const result = await presetGrpc.createPreset(projectId, {
        name: copyName.trim(),
        description: preset.description || `Copy of ${preset.name}`,
        params: preset.params,
        tag: preset.tag,
      })

      if (result.success) {
        toast.success(`Created preset "${copyName}"`)
        onCopy()
        onClose()
      } else {
        toast.error(result.error || 'Failed to create preset')
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to create preset')
    } finally {
      setIsCopying(false)
    }
  }

  const systemPrompt = preset.params?.system_prompt as string | undefined
  const otherParams = Object.entries(preset.params || {}).filter(([k]) => k !== 'system_prompt')

  return (
    <Modal
      isOpen={true}
      onClose={onClose}
      title={preset.name}
      size="lg"
      hideCloseButton={true}
      headerActions={
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={onClose}>
            Close
          </Button>
        </div>
      }
    >
      <div className="space-y-6 pb-2">
        {/* Header Badges */}
        <div className="flex items-center gap-2">
          <span className={cn(
            "text-xs px-2 py-0.5 rounded-full font-medium",
            preset.source === 'builtin' ? "bg-blue-500/10 text-blue-600" :
            preset.source === 'project' ? "bg-emerald-500/10 text-emerald-600" :
            "bg-violet-500/10 text-violet-600"
          )}>
            {preset.source === 'builtin' ? 'Built-in' : preset.source === 'project' ? 'Project' : 'Custom'}
          </span>
          {preset.tag && (
            <span className="text-xs px-2 py-0.5 rounded-full bg-muted text-muted-foreground font-medium font-mono">
              {typeof preset.tag === 'string' ? preset.tag : JSON.stringify(preset.tag)}
            </span>
          )}
        </div>

        {/* Description */}
        {preset.description && (
          <div>
            <h3 className="text-sm font-medium text-foreground mb-1.5">Description</h3>
            <p className="text-sm text-muted-foreground leading-relaxed">{preset.description}</p>
          </div>
        )}

        {/* System Prompt - Featured */}
        {systemPrompt && (
          <div>
            <div className="flex items-center justify-between mb-2">
              <h3 className="text-sm font-medium text-foreground flex items-center gap-2">
                <span className="w-1.5 h-1.5 rounded-full bg-primary/70" />
                System Prompt
              </h3>
              <Button
                variant="ghost"
                size="sm"
                className="h-6 w-6 p-0"
                onClick={() => {
                  navigator.clipboard.writeText(String(systemPrompt))
                  toast.success('System prompt copied to clipboard')
                }}
              >
                <Copy className="w-3.5 h-3.5" />
                <span className="sr-only">Copy system prompt</span>
              </Button>
            </div>
            <div className="bg-muted/50 text-foreground p-4 rounded-lg font-mono text-xs whitespace-pre-wrap max-h-[400px] overflow-y-auto border border-border/50 shadow-inner">
              {String(systemPrompt)}
            </div>
          </div>
        )}

        {/* Other Parameters */}
        {otherParams.length > 0 && (
          <div>
            <h3 className="text-sm font-medium text-foreground mb-3">Parameters</h3>
            <div className="grid gap-3 sm:grid-cols-2">
              {otherParams.map(([key, value]) => (
                <div key={key} className="bg-muted/30 border border-border/50 rounded-lg p-3">
                  <div className="text-xs font-medium text-muted-foreground uppercase tracking-wider mb-1.5">{key}</div>
                  <div className="font-mono text-xs text-foreground break-all">
                    {Array.isArray(value) ? (
                      <div className="flex flex-wrap gap-1.5">
                        {value.map((item: any, i: number) => (
                          <span key={i} className="inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium bg-muted text-muted-foreground">
                            {formatValueForDisplay(item)}
                          </span>
                        ))}
                      </div>
                    ) : typeof value === 'object' && value !== null ? (
                      <pre className="whitespace-pre-wrap text-xs bg-background/50 p-2 rounded border border-border/50">{JSON.stringify(unwrapProtoValue(value as any) ?? value, null, 2)}</pre>
                    ) : (
                      <span className="inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium bg-muted text-muted-foreground">
                        {formatValueForDisplay(value)}
                      </span>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Copy Section (Built-in only) */}
        {preset.source === 'builtin' && (
          <div className="bg-muted/30 -mx-6 -mb-6 px-6 py-4 border-t border-border/60 mt-8 flex flex-col gap-3">
            <div>
              <h4 className="text-sm font-medium text-foreground">Create Editable Copy</h4>
              <p className="text-xs text-muted-foreground mt-0.5">Start with this preset's configuration to create your own.</p>
            </div>
            <div className="flex gap-2">
              <input
                type="text"
                value={copyName}
                onChange={(e) => setCopyName(e.target.value)}
                placeholder="New preset name"
                className="flex-1 px-3 py-2 text-sm border border-border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring/20 focus:border-ring"
                onKeyDown={(e) => e.key === 'Enter' && handleCopy()}
              />
              <Button onClick={handleCopy} disabled={isCopying || !copyName.trim()}>
                {isCopying ? 'Creating...' : 'Create Copy'}
              </Button>
            </div>
          </div>
        )}
      </div>
    </Modal>
  )
}
