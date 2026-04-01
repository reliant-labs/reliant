import { Check, Loader2, AlertCircle, Circle } from 'lucide-react'
import type { SaveStatus } from '../../hooks/useAutoSave'

interface SaveStatusIndicatorProps {
  status: SaveStatus
  className?: string
}

export function SaveStatusIndicator({ status, className = '' }: SaveStatusIndicatorProps) {
  const getStatusDisplay = () => {
    switch (status) {
      case 'saved':
        return {
          icon: <Check className="w-3.5 h-3.5" />,
          text: 'Saved',
          color: 'text-green-500',
        }
      case 'saving':
        return {
          icon: <Loader2 className="w-3.5 h-3.5 animate-spin" />,
          text: 'Saving...',
          color: 'text-blue-500',
        }
      case 'unsaved':
        return {
          icon: <Circle className="w-3.5 h-3.5 fill-current" />,
          text: 'Unsaved',
          color: 'text-amber-500',
        }
      case 'error':
        return {
          icon: <AlertCircle className="w-3.5 h-3.5" />,
          text: 'Save failed',
          color: 'text-red-500',
        }
    }
  }

  const { icon, text, color } = getStatusDisplay()

  return (
    <div className={`flex items-center gap-1.5 ${color} ${className}`}>
      {icon}
      <span className="text-sm font-medium">{text}</span>
    </div>
  )
}
