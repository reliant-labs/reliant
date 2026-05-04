import { CheckCircle2, Circle, Gift, KeyRound } from 'lucide-react';
import { useState } from 'react';
import { cn } from '../../../../lib/utils';
import type { StepProps, ModelProvider } from '../../types';
import { getControlPlaneClient } from '../api';
import { LLMGatewayService } from '../gen/admin_pb';

interface ProviderOption {
  label: string;
  value: ModelProvider;
  placeholder: string;
}

const BYOK_PROVIDERS: ProviderOption[] = [
  { label: 'Anthropic', value: 'anthropic', placeholder: 'sk-ant-...' },
  { label: 'OpenAI', value: 'openai', placeholder: 'sk-...' },
  { label: 'OpenRouter', value: 'openrouter', placeholder: 'sk-or-...' },
  { label: 'Other', value: 'other', placeholder: 'Enter your API key' },
];

export function LLMKeyStep({ plan, updatePlan, onNext }: StepProps) {
  const [showBYOK, setShowBYOK] = useState(
    plan.modelProvider !== undefined &&
    plan.modelProvider !== 'reliant_credits' &&
    plan.modelProvider !== 'not_configured',
  );
  const [selectedProvider, setSelectedProvider] = useState<ModelProvider>(
    plan.modelProvider && plan.modelProvider !== 'reliant_credits' && plan.modelProvider !== 'not_configured'
      ? plan.modelProvider
      : 'anthropic',
  );
  const [apiKey, setApiKey] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleCredits = () => {
    updatePlan({ modelProvider: 'reliant_credits' });
    onNext();
  };

  const handleSaveKey = async () => {
    if (!apiKey.trim()) return;

    setSaving(true);
    setError(null);
    try {
      const client = getControlPlaneClient(LLMGatewayService);
      await client.createLLMKey({
        name: `${selectedProvider} (onboarding)`,
        models: [],
      });
      updatePlan({ modelProvider: selectedProvider });
      onNext();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save API key');
    } finally {
      setSaving(false);
    }
  };

  const currentProviderConfig = BYOK_PROVIDERS.find((p) => p.value === selectedProvider);
  const isCreditsSelected = plan.modelProvider === 'reliant_credits';
  const isBYOKSelected = showBYOK;

  return (
    <div className="space-y-6">
      <div className="text-center space-y-2">
        <h2 className="text-xl font-semibold text-foreground">
          Choose your model provider
        </h2>
        <p className="text-sm text-muted-foreground">
          Reliant uses LLMs to power its workflows. Pick how you'd like to pay for model usage.
        </p>
      </div>

      <div className="flex flex-col gap-3">
        {/* Primary: Reliant credits */}
        <button
          onClick={handleCredits}
          aria-pressed={isCreditsSelected}
          className={cn(
            'flex items-center gap-4 p-4 rounded-lg border-2 transition-all text-left',
            'hover:border-primary/50 hover:bg-muted/50',
            isCreditsSelected
              ? 'border-primary bg-primary/10 ring-1 ring-primary/30'
              : 'border-border/50 bg-background',
          )}
        >
          <div className={cn(
            'flex-shrink-0 p-2 rounded-lg',
            isCreditsSelected ? 'bg-primary/15 text-primary' : 'bg-muted text-muted-foreground',
          )}>
            <Gift className="w-5 h-5" aria-hidden="true" />
          </div>
          <div className="min-w-0 flex-1 space-y-0.5">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-sm font-medium text-foreground">
                Start with Reliant credits
              </span>
              <span className="text-[10px] font-medium uppercase tracking-wider px-1.5 py-0.5 rounded bg-primary/20 text-primary">
                $5 free
              </span>
            </div>
            <span className="block text-xs text-muted-foreground">
              Use your welcome credit to get started — no API key needed.
            </span>
          </div>
          {isCreditsSelected ? (
            <CheckCircle2 className="w-5 h-5 text-primary flex-shrink-0" aria-hidden="true" />
          ) : (
            <Circle className="w-5 h-5 text-muted-foreground/50 flex-shrink-0" aria-hidden="true" />
          )}
        </button>

        {/* Secondary: Bring your own key */}
        <button
          onClick={() => setShowBYOK(!showBYOK)}
          aria-pressed={isBYOKSelected}
          className={cn(
            'flex items-center gap-4 p-4 rounded-lg border-2 transition-all text-left',
            'hover:border-primary/50 hover:bg-muted/50',
            isBYOKSelected
              ? 'border-primary bg-primary/10 ring-1 ring-primary/30'
              : 'border-border/50 bg-background',
          )}
        >
          <div className={cn(
            'flex-shrink-0 p-2 rounded-lg',
            isBYOKSelected ? 'bg-primary/15 text-primary' : 'bg-muted text-muted-foreground',
          )}>
            <KeyRound className="w-5 h-5" aria-hidden="true" />
          </div>
          <div className="min-w-0 flex-1 space-y-0.5">
            <span className="block text-sm font-medium text-foreground">
              Bring your own key
            </span>
            <span className="block text-xs text-muted-foreground">
              Use your own API key from Anthropic, OpenAI, OpenRouter, or another provider.
            </span>
          </div>
          {isBYOKSelected ? (
            <CheckCircle2 className="w-5 h-5 text-primary flex-shrink-0" aria-hidden="true" />
          ) : (
            <Circle className="w-5 h-5 text-muted-foreground/50 flex-shrink-0" aria-hidden="true" />
          )}
        </button>
      </div>

      {/* BYOK expanded section */}
      {showBYOK && (
        <div className="space-y-4 rounded-lg border border-border/50 bg-muted/30 p-4">
          {/* Provider selector */}
          <div className="space-y-1.5">
            <label className="block text-xs text-muted-foreground">Provider</label>
            <div className="grid grid-cols-2 gap-2">
              {BYOK_PROVIDERS.map((provider) => (
                <button
                  key={provider.value}
                  onClick={() => setSelectedProvider(provider.value)}
                  className={cn(
                    'px-3 py-2 rounded-lg text-xs font-medium transition-colors border',
                    selectedProvider === provider.value
                      ? 'border-primary bg-primary/10 text-foreground'
                      : 'border-border/40 bg-background text-muted-foreground hover:text-foreground hover:border-primary/30',
                  )}
                >
                  {provider.label}
                </button>
              ))}
            </div>
          </div>

          {/* API key input */}
          <div className="space-y-1.5">
            <label htmlFor="llm-key-input" className="block text-xs text-muted-foreground">
              API key
            </label>
            <input
              id="llm-key-input"
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder={currentProviderConfig?.placeholder ?? 'Enter your API key'}
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
            onClick={handleSaveKey}
            disabled={!apiKey.trim() || saving}
            className={cn(
              'w-full py-2.5 rounded-lg text-sm font-medium transition-colors',
              apiKey.trim() && !saving
                ? 'bg-primary text-primary-foreground hover:bg-primary/90'
                : 'bg-muted text-muted-foreground cursor-not-allowed',
            )}
          >
            {saving ? 'Saving...' : 'Save & continue'}
          </button>
        </div>
      )}
    </div>
  );
}