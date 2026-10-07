import { useCallback, useState, useRef, useEffect } from 'react'
import { useEscapeLayer } from '../../hooks/useEscapeLayer'
import { ChevronDown, Check, Loader2, Search } from 'lucide-react'
import { cn } from '../../lib/utils'
import { useModels, useGlobalDataStore } from '../../store/globalDataStore'

/** Value accepted by ModelDropdown — the backend model selector format. */
export type ModelValue = { id: string } | { tags: string[] } | string | undefined

/** Extract a string model ID from any ModelValue format (returns '' for tags-based or unset). */
export function extractModelId(value: ModelValue): string {
  if (!value) return ''
  if (typeof value === 'string') return value
  if (typeof value !== 'object') return ''
  if ('id' in value && typeof value.id === 'string') return value.id
  return ''
}

interface ModelDropdownProps {
  value: ModelValue
  onChange: (value: { id: string }) => void
  disabled?: boolean
  placeholder?: string
}

export function ModelDropdown({
  value,
  onChange,
  disabled = false,
  placeholder = 'Select model...',
}: ModelDropdownProps) {
  const [isOpen, setIsOpen] = useState(false)
  const [query, setQuery] = useState('')
  const dropdownRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const { models, loading: isLoading } = useModels()
  const isInitialized = useGlobalDataStore((state) => state.isInitialized)
  const isPrefetching = useGlobalDataStore((state) => state.isPrefetching)

  const isActuallyLoading = isLoading || isPrefetching || !isInitialized

  const currentId = extractModelId(value)

  const selectedModel = models.find((m) => {
    if (m.id === currentId) return true
    const modelBaseId = m.id.split('@')[0]
    return modelBaseId === currentId
  })

  // Escape closes the list (and only the list: see useEscapeLayer).
  const closeList = useCallback(() => setIsOpen(false), [])
  const escapeLayer = useEscapeLayer(isOpen, closeList)

  // Close dropdown when clicking outside
  useEffect(() => {
    if (!isOpen) return
    searchRef.current?.focus()
    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setIsOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [isOpen])

  // Group the configured models by provider, narrowed by the search.
  const q = query.trim().toLowerCase()
  const matchingModels = q
    ? models.filter((m) => [m.name, m.id, m.provider ?? ''].some((text) => text.toLowerCase().includes(q)))
    : models
  const groupedModels = matchingModels.reduce<Record<string, typeof models>>((groups, model) => {
    const provider = model.provider || 'Other'
    if (!groups[provider]) groups[provider] = []
    groups[provider].push(model)
    return groups
  }, {})
  const providers = Object.keys(groupedModels).sort()

  // Display label: model name if recognized, otherwise raw ID or placeholder
  const displayLabel = isActuallyLoading
    ? 'Loading models...'
    : selectedModel
      ? selectedModel.name
      : currentId || placeholder

  return (
    <div ref={dropdownRef} className="relative">
      <button
        type="button"
        onClick={() => {
          if (!disabled && !isActuallyLoading) {
            setQuery('')
            setIsOpen(!isOpen)
          }
        }}
        disabled={disabled || isActuallyLoading}
        className={cn(
          'flex items-center justify-between gap-2 w-full px-2.5 py-1.5 text-xs rounded-[6px]',
          'border border-[hsl(var(--config-input-border))] bg-[hsl(var(--config-input-bg))] text-foreground',
          'focus:outline-none focus:border-ring focus:shadow-[0_0_0_2px_hsl(var(--ring)/0.15)]',
          (disabled || isActuallyLoading) && 'opacity-50 cursor-not-allowed',
        )}
      >
        <span className="truncate">{displayLabel}</span>
        {isActuallyLoading ? (
          <Loader2 className="w-4 h-4 shrink-0 opacity-50 animate-spin" />
        ) : (
          <ChevronDown className="w-4 h-4 shrink-0 opacity-50" />
        )}
      </button>

      {isOpen && (
        <div {...escapeLayer} className="absolute top-full left-0 right-0 mt-1 z-[1000] rounded-[6px] border border-border bg-card shadow-lg overflow-hidden">
          {models.length > 0 && (
            <div className="flex items-center gap-2 border-b border-border px-2.5 py-2">
              <Search className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" aria-hidden />
              <input
                ref={searchRef}
                type="search"
                aria-label="Search models"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search models"
                className="w-full bg-transparent text-xs text-foreground placeholder:text-muted-foreground focus:outline-none"
              />
            </div>
          )}
          <div className="py-1 max-h-64 overflow-y-auto">
            {providers.map((provider) => (
              <div key={provider}>
                {providers.length > 1 && (
                  <div className="px-3 py-1 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                    {provider}
                  </div>
                )}
                {groupedModels[provider].map((model) => {
                  const isSelected = currentId === model.id || model.id.split('@')[0] === currentId
                  return (
                    <button
                      key={model.id}
                      type="button"
                      onClick={() => {
                        onChange({ id: model.id })
                        setIsOpen(false)
                      }}
                      className={cn(
                        'w-full px-3 py-1.5 text-left text-xs transition-colors',
                        isSelected ? 'bg-primary/15 text-primary' : 'hover:bg-muted',
                      )}
                    >
                      <div className="flex items-center justify-between">
                        <div>
                          <div className="font-medium">{model.name}</div>
                          <div className="text-xs text-muted-foreground">{model.id}</div>
                        </div>
                        {isSelected && <Check className="w-4 h-4 shrink-0 text-primary" />}
                      </div>
                    </button>
                  )
                })}
              </div>
            ))}
            {models.length > 0 && matchingModels.length === 0 && (
              <p className="px-3 py-3 text-xs text-muted-foreground">No configured model matches “{query}”.</p>
            )}
            {models.length === 0 && !isActuallyLoading && (
              <div className="px-4 py-6 text-center space-y-2">
                <p className="text-sm text-muted-foreground">No models configured</p>
                <p className="text-xs text-muted-foreground/70">
                  Add API keys in Settings → Providers, or type a model tag like{' '}
                  <code className="bg-muted px-1 rounded font-mono text-xs">flagship</code>{' '}
                  directly using the CEL mode toggle
                </p>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}