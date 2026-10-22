import type { ComponentProps } from 'react'
import { Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { SocialProviderIcon, type SocialProvider } from './icons/SocialProviderIcon'

interface OAuthButtonProps extends Omit<ComponentProps<'button'>, 'children'> {
  provider: SocialProvider
  /**
   * This provider's sign-in is in flight. Starting a provider round-trip can
   * take seconds before the browser moves, so the button must acknowledge the
   * click — otherwise the screen looks inert and users click again.
   *
   * Pass this ONLY for the provider that was actually clicked. Driving every
   * button from one shared flag makes the app look like it is signing in with
   * a provider the user never chose.
   */
  loading?: boolean
}

const providerConfig = {
  google: {
    name: 'Google',
    bgColor: 'bg-white dark:bg-gray-800 hover:bg-gray-50 dark:hover:bg-gray-700',
    textColor: 'text-gray-700 dark:text-gray-200',
    borderColor: 'border-gray-300 dark:border-gray-600',
  },
  github: {
    name: 'GitHub',
    bgColor: 'bg-[#24292e] hover:bg-[#1b1f23] dark:bg-[#24292e] dark:hover:bg-[#1b1f23]',
    textColor: 'text-white',
    borderColor: 'border-[#24292e] dark:border-[#24292e]',
  },
  apple: {
    name: 'Apple',
    bgColor: 'bg-black hover:bg-gray-900 dark:bg-black dark:hover:bg-gray-900',
    textColor: 'text-white',
    borderColor: 'border-black dark:border-black',
  },
}

export function OAuthButton({ provider, loading = false, disabled, ...props }: OAuthButtonProps) {
  const config = providerConfig[provider]

  return (
    <button
      type="button"
      // Disabled while ANY provider is in flight (the caller passes `disabled`
      // for the others), so a second click cannot start a competing sign-in.
      disabled={loading || disabled}
      // aria-busy tracks this provider alone: a screen reader should announce
      // the one the user chose as busy, not the whole row.
      aria-busy={loading}
      data-testid={`oauth-button-${provider}`}
      className={cn(
        'w-full inline-flex justify-center items-center gap-3 py-2.5 px-4',
        'border rounded-lg shadow-sm text-sm font-medium',
        'transition-colors duration-200',
        'disabled:opacity-50 disabled:cursor-not-allowed',
        'focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2',
        config.bgColor,
        config.textColor,
        config.borderColor,
      )}
      {...props}
    >
      {loading ? (
        <Loader2 className="w-5 h-5 animate-spin" aria-hidden="true" />
      ) : (
        <SocialProviderIcon provider={provider} />
      )}
      <span>
        {loading ? `Connecting to ${config.name}...` : `Continue with ${config.name}`}
      </span>
    </button>
  )
}
