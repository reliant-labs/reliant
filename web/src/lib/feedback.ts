import { supabase } from './supabase'

// =============================================================================
// TYPES
// =============================================================================

// Using const array to make FeedbackType available at runtime (type-only exports are stripped by Vite)
export const FEEDBACK_TYPES = ['bug', 'feature', 'general'] as const
export type FeedbackType = typeof FEEDBACK_TYPES[number]

export interface FeedbackSubmission {
  type: FeedbackType
  title: string
  description: string
  attachments?: File[]
  extraContext?: Record<string, unknown>
}

export interface FeedbackRecord {
  id: string
  user_id: string | null
  type: FeedbackType
  title: string
  description: string
  app_version: string | null
  os_info: string | null
  user_agent: string | null
  current_url: string | null
  extra_context: Record<string, unknown>
  created_at: string
  updated_at: string
}

export interface FeedbackAttachment {
  id: string
  feedback_id: string
  storage_path: string
  file_name: string
  file_size: number | null
  mime_type: string | null
  created_at: string
}

export interface SubmitFeedbackResult {
  success: boolean
  feedbackId?: string
  error?: string
}

// =============================================================================
// SYSTEM INFO
// =============================================================================

export interface SystemInfo {
  appVersion: string | null
  osInfo: string | null
  userAgent: string
  currentUrl: string
}

/**
 * Collects system information for feedback context (sync version for display)
 */
export function getSystemInfo(): SystemInfo {
  // Parse OS info from user agent
  const userAgent = navigator.userAgent
  let osInfo: string | null = null
  
  if (userAgent.includes('Mac OS X')) {
    const match = userAgent.match(/Mac OS X (\d+[._]\d+[._]?\d*)/)
    osInfo = match ? `macOS ${match[1].replace(/_/g, '.')}` : 'macOS'
  } else if (userAgent.includes('Windows')) {
    const match = userAgent.match(/Windows NT (\d+\.\d+)/)
    osInfo = match ? `Windows ${match[1]}` : 'Windows'
  } else if (userAgent.includes('Linux')) {
    osInfo = 'Linux'
  }
  
  return {
    appVersion: null, // Will be fetched async
    osInfo,
    userAgent,
    currentUrl: window.location.href,
  }
}

/**
 * Collects system information including app version (async)
 */
export async function getSystemInfoAsync(): Promise<SystemInfo> {
  const baseInfo = getSystemInfo()
  
  // Try to get app version from Electron
  let appVersion: string | null = null
  try {
    if (window.electronAPI?.getVersion) {
      appVersion = await window.electronAPI.getVersion()
    }
  } catch (error) {
    console.warn('[Feedback] Failed to get app version:', error)
  }
  
  return {
    ...baseInfo,
    appVersion,
  }
}

// =============================================================================
// API FUNCTIONS
// =============================================================================

/**
 * Uploads a file to Supabase storage for feedback attachments
 */
async function uploadAttachment(
  file: File,
  userId: string | null
): Promise<{ path: string; error: string | null }> {
  // Use user ID or 'anonymous' as folder
  const folder = userId ?? 'anonymous'
  const timestamp = Date.now()
  const safeName = file.name.replace(/[^a-zA-Z0-9.-]/g, '_')
  const path = `${folder}/${timestamp}-${safeName}`
  
  const { error } = await supabase.storage
    .from('feedback-attachments')
    .upload(path, file, {
      cacheControl: '3600',
      upsert: false,
    })
  
  if (error) {
    console.error('[Feedback] Upload error:', error)
    return { path: '', error: error.message }
  }
  
  return { path, error: null }
}

/**
 * Submits feedback to Supabase
 */
export async function submitFeedback(
  submission: FeedbackSubmission
): Promise<SubmitFeedbackResult> {
  try {
    // Get current user (may be null for anonymous)
    const { data: { user } } = await supabase.auth.getUser()
    const userId = user?.id ?? null
    
    // Collect system info (async to get app version)
    const systemInfo = await getSystemInfoAsync()
    
    // Insert feedback record
    const { data: feedback, error: feedbackError } = await supabase
      .from('feedback')
      .insert({
        user_id: userId,
        type: submission.type,
        title: submission.title,
        description: submission.description,
        app_version: systemInfo.appVersion,
        os_info: systemInfo.osInfo,
        user_agent: systemInfo.userAgent,
        current_url: systemInfo.currentUrl,
        extra_context: submission.extraContext ?? {},
      })
      .select('id')
      .single()
    
    if (feedbackError) {
      console.error('[Feedback] Insert error:', feedbackError)
      return {
        success: false,
        error: feedbackError.message,
      }
    }
    
    const feedbackId = feedback.id
    
    // Upload attachments if any
    if (submission.attachments && submission.attachments.length > 0) {
      const attachmentPromises = submission.attachments.map(async (file) => {
        const { path, error } = await uploadAttachment(file, userId)
        
        if (error) {
          console.warn(`[Feedback] Failed to upload ${file.name}:`, error)
          return null
        }
        
        // Insert attachment record
        const { error: attachmentError } = await supabase
          .from('feedback_attachments')
          .insert({
            feedback_id: feedbackId,
            storage_path: path,
            file_name: file.name,
            file_size: file.size,
            mime_type: file.type,
          })
        
        if (attachmentError) {
          console.warn(`[Feedback] Failed to save attachment record:`, attachmentError)
          return null
        }
        
        return path
      })
      
      await Promise.all(attachmentPromises)
    }
    
    return {
      success: true,
      feedbackId,
    }
  } catch (error) {
    console.error('[Feedback] Unexpected error:', error)
    return {
      success: false,
      error: error instanceof Error ? error.message : 'An unexpected error occurred',
    }
  }
}

/**
 * Get feedback history for the current user
 */
export async function getUserFeedback(): Promise<{
  feedback: FeedbackRecord[]
  error: string | null
}> {
  const { data, error } = await supabase
    .from('feedback')
    .select('*')
    .order('created_at', { ascending: false })
  
  if (error) {
    return { feedback: [], error: error.message }
  }
  
  return { feedback: data ?? [], error: null }
}

// =============================================================================
// UI HELPERS
// =============================================================================

export const FEEDBACK_TYPE_LABELS: Record<FeedbackType, string> = {
  bug: 'Bug Report',
  feature: 'Feature Request',
  general: 'General Feedback',
}

export const FEEDBACK_TYPE_DESCRIPTIONS: Record<FeedbackType, string> = {
  bug: 'Report something that isn\'t working correctly',
  feature: 'Suggest a new feature or improvement',
  general: 'Share your thoughts or ask a question',
}

export const FEEDBACK_TYPE_PLACEHOLDERS: Record<FeedbackType, { title: string; description: string }> = {
  bug: {
    title: 'e.g., Chat messages not loading after refresh',
    description: 'Please describe what happened, what you expected, and steps to reproduce the issue.',
  },
  feature: {
    title: 'e.g., Add dark mode support for code blocks',
    description: 'Please describe the feature you\'d like and how it would help your workflow.',
  },
  general: {
    title: 'e.g., Question about workflow configuration',
    description: 'Share your feedback, questions, or suggestions.',
  },
}
