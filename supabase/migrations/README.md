# Supabase Migrations

This directory contains SQL migrations for the Supabase database. Each migration file contains its own schema details.

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
psql "postgresql://postgres:[PASSWORD]@db.[PROJECT_REF].supabase.co:5432/postgres" \
  -f supabase/migrations/<migration_file>.sql
```

## Troubleshooting

### "relation already exists"

The migration was already applied. Safe to ignore.

### Storage bucket conflicts

Bucket creation uses `ON CONFLICT DO NOTHING` to handle existing buckets gracefully.

### RLS policy conflicts

If policy names conflict, drop existing policies first:

```sql
DROP POLICY IF EXISTS "policy_name" ON public.table_name;
```
