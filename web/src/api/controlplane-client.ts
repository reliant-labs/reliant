import { supabase } from '@/lib/supabase'
import { getAdminURL } from '@/lib/constants'

// Control plane GitCredentialService client using Connect JSON protocol
async function cpFetch(method: string, body: Record<string, unknown> = {}) {
  const adminURL = getAdminURL()
  if (!adminURL) throw new Error('Admin API URL not configured')
  
  const { data: { session } } = await supabase.auth.getSession()
  if (!session) throw new Error('No active session')

  const resp = await fetch(`${adminURL}/controlplane.v1.GitCredentialService/${method}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${session.access_token}`,
    },
    body: JSON.stringify(body),
  })

  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`${method} failed (${resp.status}): ${text}`)
  }

  return resp.json()
}

export interface GitAccount {
  provider: string
  account_login: string
  scopes: string
  created_at?: string
  updated_at?: string
}

export async function saveGitCredential(
  provider: string,
  accessToken: string,
  scopes: string,
  accountLogin?: string,
): Promise<{ account_login: string }> {
  return cpFetch('SaveGitCredential', {
    provider,
    access_token: accessToken,
    scopes,
    account_login: accountLogin ?? '',
  })
}

export async function getGitCredential(
  provider: string,
  accountLogin?: string,
): Promise<{
  provider: string
  scopes: string
  has_token: boolean
  account_login?: string
  created_at?: string
  updated_at?: string
}> {
  return cpFetch('GetGitCredential', { provider, account_login: accountLogin ?? '' })
}

export async function listGitCredentials(provider?: string): Promise<{ accounts: GitAccount[] }> {
  return cpFetch('ListGitCredentials', { provider: provider ?? '' })
}

export async function deleteGitCredential(provider: string, accountLogin: string) {
  return cpFetch('DeleteGitCredential', { provider, account_login: accountLogin })
}

export async function cloneRepo(
  daemonName: string,
  gitRepo: string,
  gitBranch: string,
  path: string,
  accountLogin?: string,
): Promise<{ cloned_path: string }> {
  return cpFetch('CloneRepo', {
    daemon_name: daemonName,
    git_repo: gitRepo,
    git_branch: gitBranch,
    path,
    account_login: accountLogin ?? '',
  })
}
