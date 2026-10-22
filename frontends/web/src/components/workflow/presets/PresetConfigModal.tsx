// Copyright (c) 2025 Reliant Labs

/**
 * Choose a workflow's default preset per preset group. Extracted unchanged
 * from the retired WorkflowHub (WORKFLOW_UI.md §1.2, decision 7); opened from
 * workflow detail's Presets section.
 */

import { useState, useEffect } from 'react'
import { toast } from 'sonner'
import { Modal } from '../../ui/Modal'
import { Button } from '../../ui/Button'
import type { Preset } from '../../../store/globalDataStore'
import { presetGrpc } from '../../../api/preset-grpc'
import { workflowGrpc } from '../../../api/workflow-grpc'
import { normalizeWorkflowRef } from '../useWorkflowInputs'
import { usePreferencesStore } from '../../../store/preferencesStore'
import type { InputDef } from '../../../lib/inputHelpers'
import { getInputPresetConfig } from '../../../lib/inputHelpers'
interface PresetConfigModalProps {
  workflowName: string
  projectId: string
  availablePresets: Preset[]
  onSave: () => void
  onClose: () => void
}

interface GroupInfo {
  name: string  // "" for top-level
  label: string
  tag?: string
}

export function PresetConfigModal({
  workflowName,
  projectId,
  availablePresets,
  onSave,
  onClose
}: PresetConfigModalProps) {
  const [groups, setGroups] = useState<GroupInfo[]>([])
  const [selectedPresets, setSelectedPresets] = useState<Record<string, string>>({})
  const [isLoading, setIsLoading] = useState(true)
  const [isSaving, setIsSaving] = useState(false)
  const { preferences, loadPreferences } = usePreferencesStore()

  // Ensure preferences are loaded when modal opens
  useEffect(() => {
    if (!preferences) {
      loadPreferences()
    }
  }, [preferences, loadPreferences])

  // Load workflow definition and current defaults
  useEffect(() => {
    const load = async () => {
      setIsLoading(true)
      try {
        // Wait for preferences to be loaded if they're not already
        if (!preferences) {
          await loadPreferences()
        }

        // Fetch workflow definition to get groups
        const result = await workflowGrpc.getWorkflow(projectId, { name: workflowName })
        const workflow = result.workflow
        if (!workflow) {
          throw new Error('Workflow not found')
        }

        const groupInfos: GroupInfo[] = []

        // Add top-level if it has a tag
        if (workflow.presets?.tag) {
          groupInfos.push({ name: "", label: "Top-level", tag: workflow.presets.tag })
        }

        // Add named groups (inputs with type: "group")
        const workflowInputs = workflow.inputs
        if (workflowInputs) {
          for (const [groupName, param] of Object.entries(workflowInputs)) {
            const presetCfg = getInputPresetConfig(param as InputDef)
            if (param.type === "group" && presetCfg?.tag) {
              groupInfos.push({ name: groupName, label: groupName, tag: presetCfg.tag })
            }
          }
        }

        setGroups(groupInfos)

        // Fetch current defaults
        const defaults = await presetGrpc.getDefaultPresets(projectId, workflowName)
        setSelectedPresets(defaults)
      } catch (error) {
        console.error('Failed to load workflow config:', error)
        toast.error('Failed to load workflow configuration')
      } finally {
        setIsLoading(false)
      }
    }
    load()
  }, [projectId, workflowName, preferences, loadPreferences])

  const handleSave = async () => {
    setIsSaving(true)
    try {
      // Save each group's default
      for (const group of groups) {
        const presetName = selectedPresets[group.name] || null
        await presetGrpc.setDefaultPreset(projectId, workflowName, group.name, presetName)
      }
      onSave()
      onClose()
      toast.success('Default presets saved')
    } catch {
      toast.error('Failed to save preset configuration')
    } finally {
      setIsSaving(false)
    }
  }

  // Get presets that match a tag
  const getPresetsForTag = (tag?: string): Preset[] => {
    if (!tag) return []
    return availablePresets.filter(p => p.tag === tag)
  }

  const displayName = normalizeWorkflowRef(workflowName)

  return (
    <Modal
      isOpen={true}
      onClose={onClose}
      title={`Default Presets: ${displayName}`}
      size="md"
    >
      <div className="space-y-4">
        <p className="text-sm text-muted-foreground">
          Select the default preset for each group. These presets will be automatically applied when starting a new chat.
        </p>

        {isLoading ? (
          <div className="py-4 text-center text-sm text-muted-foreground">
            Loading...
          </div>
        ) : groups.length === 0 ? (
          <div className="py-4 text-center text-sm text-muted-foreground">
            This workflow has no configurable preset groups.
          </div>
        ) : (
          <div className="space-y-4">
            {groups.map(group => {
              const groupPresets = getPresetsForTag(group.tag)
              return (
                <div key={group.name || '_toplevel'}>
                  <label className="block text-sm font-medium text-foreground mb-2">
                    {group.label}
                    {group.tag && (
                      <span className="ml-2 text-xs text-muted-foreground font-normal">
                        (tag: {typeof group.tag === 'string' ? group.tag : JSON.stringify(group.tag)})
                      </span>
                    )}
                  </label>
                  <select
                    value={selectedPresets[group.name] || ''}
                    onChange={(e) => setSelectedPresets(prev => ({
                      ...prev,
                      [group.name]: e.target.value
                    }))}
                    className="w-full px-3 py-2 text-sm border border-border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring/20 focus:border-ring"
                  >
                    <option value="">No default (use system default)</option>
                    {groupPresets.map(preset => (
                      <option key={preset.name} value={preset.name}>
                        {preset.name} ({preset.source})
                      </option>
                    ))}
                  </select>
                </div>
              )
            })}
          </div>
        )}

        <div className="flex justify-end gap-2 pt-4 border-t border-border">
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={handleSave} disabled={isSaving || isLoading}>
            {isSaving ? 'Saving...' : 'Save'}
          </Button>
        </div>
      </div>
    </Modal>
  )
}

