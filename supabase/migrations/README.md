# Supabase Migrations

This directory contains SQL migrations for the Supabase database.

## Migration Files

| Migration | Description |
|-----------|-------------|
| `20260115091152_create_feedback_tables.sql` | Creates feedback collection system (tables, storage bucket, RLS policies) |

## Applying Migrations

### Local Development

Migrations are automatically applied when running Supabase locally:

```bash
# Start Supabase (applies migrations automatically)
supabase start

# Or reset database and reapply all migrations
supabase db reset
```

### Production (Supabase Hosted)

**Option 1: Supabase CLI (Recommended)**

```bash
# Link to your Supabase project (one-time setup)
supabase link --project-ref YOUR_PROJECT_REF

# Push migrations to production
supabase db push
```

**Option 2: Dashboard SQL Editor**

1. Go to [Supabase Dashboard](https://app.supabase.com)
2. Select your project
3. Navigate to **SQL Editor**
4. Copy the migration SQL and run it

**Option 3: Direct Database Connection**

```bash
# Connect via psql (get connection string from Dashboard > Settings > Database)
psql "postgresql://postgres:[PASSWORD]@db.[PROJECT_REF].supabase.co:5432/postgres" \
  -f supabase/migrations/20260115091152_create_feedback_tables.sql
```

## Migration: Feedback Tables

The feedback migration (`20260115091152_create_feedback_tables.sql`) creates:

### Tables

- **`feedback`** - Stores feedback submissions
  - `id` (UUID) - Primary key
  - `user_id` (UUID) - References auth.users (nullable for anonymous)
  - `type` - bug | feature | general
  - `title`, `description` - Feedback content
  - `app_version`, `os_info`, `user_agent` - System info
  - `extra_context` (JSONB) - Additional context
  - Timestamps

- **`feedback_attachments`** - Links feedback to uploaded files
  - `id` (UUID) - Primary key
  - `feedback_id` (UUID) - References feedback
  - `storage_path` - Path in Supabase Storage
  - `file_name`, `file_size`, `mime_type` - File metadata

### Storage Bucket

- **`feedback-attachments`** - Private bucket for uploaded files
  - 10MB file size limit
  - Allowed types: images, PDF, text, JSON, CSV, ZIP

### Row Level Security (RLS)

- Users can submit feedback (authenticated or anonymous)
- Users can only view their own feedback
- Attachments follow parent feedback permissions

## Troubleshooting

### Migration Already Applied

If you get "relation already exists" errors, the migration was already applied. This is safe to ignore.

### Storage Bucket Exists

The bucket creation uses `ON CONFLICT DO NOTHING` to handle existing buckets gracefully.

### RLS Policy Conflicts

If policy names conflict, you may need to drop existing policies first:

```sql
DROP POLICY IF EXISTS "Users can insert feedback" ON public.feedback;
-- ... repeat for other policies
```
