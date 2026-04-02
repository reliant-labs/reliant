import { useState, useCallback, useEffect, useRef } from 'react'
import { Modal } from '../ui/Modal'
import { Button } from '../ui/Button'
import { LightweightCodeViewer } from '../Chat/LightweightCodeViewer'
import { Pencil, Eye, Copy, Check, AlertTriangle } from 'lucide-react'
import type { Workflow } from '../../types/workflow'
import { workflowGrpc } from '../../api/workflow-grpc'
import { toast } from 'sonner'

interface YamlEditorModalProps {
  isOpen: boolean
  onClose: () => void
  workflow: Workflow
  onApply: (workflow: Workflow) => void
  isReadOnly?: boolean
  /** Canonical YAML from the backend. When available, used instead of the frontend serializer. */
  yamlDefinition?: string
  /** Project ID needed for backend import/export calls. */
  projectId: string
}

export function YamlEditorModal({
  isOpen,
  onClose,
  workflow: _workflow,
  onApply,
  isReadOnly = false,
  yamlDefinition,
  projectId,
}: YamlEditorModalProps) {
  const [mode, setMode] = useState<'view' | 'edit'>('view')
  const [yamlContent, setYamlContent] = useState('')
  const [editedContent, setEditedContent] = useState('')
  const [parseError, setParseError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const wasOpenRef = useRef(false)

  // Initialize YAML content only when the modal first opens (not on every workflow reference change).
  // Re-running on workflow prop changes while the modal is already open would reset edited content
  // and flash the view mode, causing visible jitter.
  useEffect(() => {
    if (isOpen && !wasOpenRef.current) {
      // Modal just opened — initialize from backend YAML.
      const yaml = yamlDefinition || '# Save workflow to view YAML'
      setYamlContent(yaml)
      setEditedContent(yaml)
      setParseError(null)
      setMode('view')
    }
    wasOpenRef.current = isOpen
  }, [isOpen, yamlDefinition])

  const handleCopy = useCallback(async () => {
    const contentToCopy = mode === 'edit' ? editedContent : yamlContent
    await navigator.clipboard.writeText(contentToCopy)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }, [mode, editedContent, yamlContent])

  const handleApply = useCallback(async () => {
    try {
      setParseError(null)
      const result = await workflowGrpc.importWorkflow(projectId, editedContent, true)
      if (!result.success || !result.workflow) {
        const errMsg = result.validationErrors?.length
          ? result.validationErrors.map(e => e.message).join('; ')
          : result.message || 'Failed to import YAML'
        setParseError(errMsg)
        return
      }
      onApply(result.workflow)
      toast.success('Workflow updated from YAML')
      onClose()
    } catch (err) {
      const errMsg = err instanceof Error ? err.message : 'Unknown error'
      setParseError(errMsg)
      toast.error(`Failed to parse YAML: ${errMsg}`)
    }
  }, [editedContent, onApply, onClose, projectId])

  const handleModeSwitch = useCallback((newMode: 'view' | 'edit') => {
    if (newMode === 'edit' && mode === 'view') {
      // Switching to edit - sync content
      setEditedContent(yamlContent)
      setParseError(null)
    }
    setMode(newMode)
  }, [mode, yamlContent])

  const handleDiscard = useCallback(() => {
    setEditedContent(yamlContent)
    setParseError(null)
    setMode('view')
  }, [yamlContent])

  const hasChanges = mode === 'edit' && editedContent !== yamlContent

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title="Workflow YAML"
      size="xl"
    >
      <div className="flex flex-col h-[70vh]">
        {/* Toolbar */}
        <div className="flex items-center justify-between pb-3 border-b border-border">
          <div className="flex items-center gap-2">
            {/* Mode Toggle */}
            <div className="flex rounded-lg bg-muted p-0.5">
              <button
                onClick={() => handleModeSwitch('view')}
                className={`flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm font-medium transition-colors ${
                  mode === 'view'
                    ? 'bg-background text-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground'
                }`}
              >
                <Eye className="w-4 h-4" />
                View
              </button>
              {!isReadOnly && (
                <button
                  onClick={() => handleModeSwitch('edit')}
                  className={`flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm font-medium transition-colors ${
                    mode === 'edit'
                      ? 'bg-background text-foreground shadow-sm'
                      : 'text-muted-foreground hover:text-foreground'
                  }`}
                >
                  <Pencil className="w-4 h-4" />
                  Edit
                </button>
              )}
            </div>
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={handleCopy}
            >
              {copied ? (
                <Check className="w-4 h-4" />
              ) : (
                <Copy className="w-4 h-4" />
              )}
            </Button>
          </div>
        </div>

        {/* Error Banner */}
        {parseError && mode === 'edit' && (
          <div className="flex items-start gap-2 p-3 mt-3 rounded-lg bg-destructive/10 border border-destructive/20 text-destructive text-sm">
            <AlertTriangle className="w-4 h-4 mt-0.5 flex-shrink-0" />
            <div>
              <div className="font-medium">YAML Parse Error</div>
              <div className="text-xs mt-0.5 opacity-90">{parseError}</div>
            </div>
          </div>
        )}

        {/* Content Area - min-h-0 is required for flexbox children to shrink properly and allow scrolling.
            overscroll-contain prevents scroll chaining to the modal backdrop / workflow canvas. */}
        <div className="flex-1 mt-3 min-h-0">
          {mode === 'view' ? (
            <div className="h-full overflow-auto overscroll-contain rounded-md border border-border">
              <LightweightCodeViewer
                content={yamlContent}
                language="yaml"
                maxHeight={10000}  // Large value to allow parent to control scrolling
                showLineNumbers={true}
                wordWrap={true}
                noBorder={true}
              />
            </div>
          ) : (
            <textarea
              value={editedContent}
              onChange={(e) => setEditedContent(e.target.value)}
              className="w-full h-full p-3 font-mono text-sm bg-muted/50 border border-border rounded-lg resize-none overscroll-contain focus:outline-none focus:ring-2 focus:ring-ring/20 focus:border-ring"
              spellCheck={false}
              placeholder="Enter YAML content..."
            />
          )}
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between pt-3 mt-3 border-t border-border">
          <div className="text-xs text-muted-foreground">
            {mode === 'edit' && hasChanges && (
              <span className="text-amber-500">Unsaved changes</span>
            )}
          </div>
          <div className="flex items-center gap-2">
            {mode === 'edit' && hasChanges && (
              <Button variant="outline" onClick={handleDiscard}>
                Discard
              </Button>
            )}
            {mode === 'edit' ? (
              <Button
                onClick={handleApply}
                disabled={!!parseError || !hasChanges}
              >
                Apply Changes
              </Button>
            ) : (
              <Button variant="outline" onClick={onClose}>
                Close
              </Button>
            )}
          </div>
        </div>
      </div>
    </Modal>
  )
}