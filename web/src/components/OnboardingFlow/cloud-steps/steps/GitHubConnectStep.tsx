import { useState } from 'react';
import { cn } from '../../../../lib/utils';
import type { StepProps } from '../../types';
import { getControlPlaneClient } from '../api';
import { GitCredentialService } from '../gen/admin_connect';

type Phase = 'pat' | 'repo';

export function GitHubConnectStep({ plan, updatePlan, onNext, onBack }: StepProps) {
  const [phase, setPhase] = useState<Phase>('pat');
  const [pat, setPat] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [repoUrl, setRepoUrl] = useState(plan.repo?.url ?? '');
  const [branch, setBranch] = useState(plan.repo?.branch ?? '');

  const handleSavePat = async () => {
    if (!pat.trim()) return;

    setSaving(true);
    setError(null);
    try {
      const client = getControlPlaneClient(GitCredentialService);
      await client.saveGitCredential({
        provider: 'github',
        accessToken: pat.trim(),
        scopes: 'repo',
      });
      setPhase('repo');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save token');
    } finally {
      setSaving(false);
    }
  };

  const handleSaveRepo = () => {
    if (!repoUrl.trim()) return;

    updatePlan({
      repo: {
        provider: 'github',
        url: repoUrl.trim(),
        branch: branch.trim() || undefined,
      },
    });
    onNext();
  };

  if (phase === 'pat') {
    return (
      <div className="space-y-6">
        <div className="text-center space-y-2">
          <h2 className="text-xl font-semibold text-foreground">
            Connect your GitHub repos
          </h2>
          <p className="text-sm text-muted-foreground">
            To clone your private repos on cloud compute, Reliant needs a GitHub personal access token.
          </p>
        </div>

        <div className="space-y-4">
          <a
            href="https://github.com/settings/tokens/new?scopes=repo&description=Reliant"
            target="_blank"
            rel="noopener noreferrer"
            className="flex items-center justify-center gap-2 w-full py-2.5 rounded-lg text-sm font-medium transition-colors border border-border/40 bg-background hover:bg-muted/50 text-foreground"
          >
            <span role="img" aria-label="GitHub">🔑</span>
            Create a token on GitHub →
          </a>

          <div className="space-y-1.5">
            <label htmlFor="pat-input" className="block text-xs text-muted-foreground">
              Personal access token
            </label>
            <input
              id="pat-input"
              type="password"
              value={pat}
              onChange={(e) => setPat(e.target.value)}
              placeholder="ghp_xxxxxxxxxxxxxxxxxxxx"
              className={cn(
                'w-full px-3 py-2.5 rounded-lg text-sm font-mono transition-colors',
                'bg-background border border-border/40 text-foreground placeholder:text-muted-foreground/50',
                'focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/50',
              )}
            />
          </div>

          {error && (
            <p className="text-xs text-destructive">{error}</p>
          )}

          <button
            onClick={handleSavePat}
            disabled={!pat.trim() || saving}
            className={cn(
              'w-full py-3 rounded-lg text-sm font-semibold transition-colors',
              pat.trim() && !saving
                ? 'bg-primary text-primary-foreground hover:bg-primary/90'
                : 'bg-muted text-muted-foreground cursor-not-allowed',
            )}
          >
            {saving ? 'Saving...' : 'Save & continue'}
          </button>
        </div>

        <button
          onClick={onBack}
          className="w-full text-center text-xs text-muted-foreground hover:text-foreground transition-colors py-1"
        >
          ← Back
        </button>
      </div>
    );
  }

  // Phase: repo
  return (
    <div className="space-y-6">
      <div className="text-center space-y-2">
        <h2 className="text-xl font-semibold text-foreground">
          Which repository?
        </h2>
        <p className="text-sm text-muted-foreground">
          Enter the GitHub URL of the repo you want to work on.
        </p>
      </div>

      <div className="space-y-4">
        <div className="space-y-1.5">
          <label htmlFor="repo-url" className="block text-xs text-muted-foreground">
            Repository URL
          </label>
          <input
            id="repo-url"
            type="url"
            value={repoUrl}
            onChange={(e) => setRepoUrl(e.target.value)}
            placeholder="https://github.com/owner/repo"
            className={cn(
              'w-full px-3 py-2.5 rounded-lg text-sm transition-colors',
              'bg-background border border-border/40 text-foreground placeholder:text-muted-foreground/50',
              'focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/50',
            )}
          />
        </div>

        <div className="space-y-1.5">
          <label htmlFor="branch-input" className="block text-xs text-muted-foreground">
            Branch (optional)
          </label>
          <input
            id="branch-input"
            type="text"
            value={branch}
            onChange={(e) => setBranch(e.target.value)}
            placeholder="main"
            className={cn(
              'w-full px-3 py-2.5 rounded-lg text-sm transition-colors',
              'bg-background border border-border/40 text-foreground placeholder:text-muted-foreground/50',
              'focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/50',
            )}
          />
        </div>

        <button
          onClick={handleSaveRepo}
          disabled={!repoUrl.trim()}
          className={cn(
            'w-full py-3 rounded-lg text-sm font-semibold transition-colors',
            repoUrl.trim()
              ? 'bg-primary text-primary-foreground hover:bg-primary/90'
              : 'bg-muted text-muted-foreground cursor-not-allowed',
          )}
        >
          Continue
        </button>
      </div>

      <button
        onClick={() => setPhase('pat')}
        className="w-full text-center text-xs text-muted-foreground hover:text-foreground transition-colors py-1"
      >
        ← Back to token
      </button>
    </div>
  );
}