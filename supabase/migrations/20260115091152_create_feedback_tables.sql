-- Migration: Create feedback tables and storage bucket
-- This migration sets up the feedback collection system for bug reports,
-- feature requests, and general feedback with attachment support.

-- =============================================================================
-- FEEDBACK TABLE
-- =============================================================================
-- Stores user feedback submissions
CREATE TABLE IF NOT EXISTS public.feedback (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID REFERENCES auth.users(id) ON DELETE SET NULL,
  type TEXT NOT NULL CHECK (type IN ('bug', 'feature', 'general')),
  title TEXT NOT NULL,
  description TEXT NOT NULL,
  
  -- System information (auto-captured)
  app_version TEXT,
  os_info TEXT,
  user_agent TEXT,
  
  -- Additional context
  current_url TEXT,
  extra_context JSONB DEFAULT '{}',
  
  -- Timestamps
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Index for querying by user
CREATE INDEX IF NOT EXISTS idx_feedback_user_id ON public.feedback(user_id);

-- Index for querying by type
CREATE INDEX IF NOT EXISTS idx_feedback_type ON public.feedback(type);

-- Index for ordering by date
CREATE INDEX IF NOT EXISTS idx_feedback_created_at ON public.feedback(created_at DESC);

-- =============================================================================
-- FEEDBACK ATTACHMENTS TABLE
-- =============================================================================
-- Junction table linking feedback to uploaded files in storage
CREATE TABLE IF NOT EXISTS public.feedback_attachments (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  feedback_id UUID NOT NULL REFERENCES public.feedback(id) ON DELETE CASCADE,
  
  -- File information
  storage_path TEXT NOT NULL,  -- Path in Supabase Storage
  file_name TEXT NOT NULL,     -- Original filename
  file_size INTEGER,           -- Size in bytes
  mime_type TEXT,              -- MIME type
  
  created_at TIMESTAMPTZ DEFAULT NOW()
);

-- Index for querying attachments by feedback
CREATE INDEX IF NOT EXISTS idx_feedback_attachments_feedback_id 
  ON public.feedback_attachments(feedback_id);

-- =============================================================================
-- ROW LEVEL SECURITY
-- =============================================================================

-- Enable RLS on both tables
ALTER TABLE public.feedback ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.feedback_attachments ENABLE ROW LEVEL SECURITY;

-- Feedback policies
-- Users can insert their own feedback (or anonymous feedback)
CREATE POLICY "Users can insert feedback"
  ON public.feedback FOR INSERT
  WITH CHECK (
    auth.uid() IS NULL OR  -- Allow anonymous feedback
    auth.uid() = user_id   -- Or authenticated user's own feedback
  );

-- Users can view their own feedback
CREATE POLICY "Users can view own feedback"
  ON public.feedback FOR SELECT
  USING (auth.uid() = user_id);

-- Attachment policies
-- Users can insert attachments for their own feedback
CREATE POLICY "Users can insert attachments for own feedback"
  ON public.feedback_attachments FOR INSERT
  WITH CHECK (
    EXISTS (
      SELECT 1 FROM public.feedback f
      WHERE f.id = feedback_id
      AND (f.user_id IS NULL OR f.user_id = auth.uid())
    )
  );

-- Users can view attachments for their own feedback
CREATE POLICY "Users can view own feedback attachments"
  ON public.feedback_attachments FOR SELECT
  USING (
    EXISTS (
      SELECT 1 FROM public.feedback f
      WHERE f.id = feedback_id
      AND f.user_id = auth.uid()
    )
  );

-- =============================================================================
-- STORAGE BUCKET
-- =============================================================================
-- Create storage bucket for feedback attachments
-- Note: This uses Supabase's storage API - bucket creation via SQL
INSERT INTO storage.buckets (id, name, public, file_size_limit, allowed_mime_types)
VALUES (
  'feedback-attachments',
  'feedback-attachments',
  false,  -- Private bucket
  10485760,  -- 10MB limit per file
  ARRAY['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'text/plain', 'application/json', 'application/pdf', 'text/csv', 'application/zip']::text[]
)
ON CONFLICT (id) DO NOTHING;

-- Storage policies for feedback-attachments bucket
-- Allow authenticated users to upload to their own folder
CREATE POLICY "Users can upload feedback attachments"
  ON storage.objects FOR INSERT
  WITH CHECK (
    bucket_id = 'feedback-attachments'
    AND (
      auth.uid()::text = (storage.foldername(name))[1]
      OR (storage.foldername(name))[1] = 'anonymous'
    )
  );

-- Allow users to read their own attachments
CREATE POLICY "Users can read own feedback attachments"
  ON storage.objects FOR SELECT
  USING (
    bucket_id = 'feedback-attachments'
    AND (
      auth.uid()::text = (storage.foldername(name))[1]
      OR (storage.foldername(name))[1] = 'anonymous'
    )
  );

-- =============================================================================
-- UPDATED_AT TRIGGER
-- =============================================================================
-- Automatically update updated_at timestamp
CREATE OR REPLACE FUNCTION public.update_feedback_updated_at()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER feedback_updated_at
  BEFORE UPDATE ON public.feedback
  FOR EACH ROW
  EXECUTE FUNCTION public.update_feedback_updated_at();
