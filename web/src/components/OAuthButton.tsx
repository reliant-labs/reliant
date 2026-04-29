import type { ComponentProps } from 'react'
import { SocialProviderIcon, type SocialProvider } from './icons/SocialProviderIcon'

interface OAuthButtonProps extends Omit<ComponentProps<'button'>, 'children'> {
  provider: SocialProvider
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
      disabled={loading || disabled}
      className={`
        w-full inline-flex justify-center items-center gap-3 py-2.5 px-4 
        border rounded-lg shadow-sm text-sm font-medium 
        transition-colors duration-200
        disabled:opacity-50 disabled:cursor-not-allowed
        focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2
        ${config.bgColor}
        ${config.textColor}
        ${config.borderColor}
      `}
      {...props}
    >
      {loading ? (
        <div className="w-5 h-5 border-2 border-current border-t-transparent rounded-full animate-spin" />
      ) : (
        <SocialProviderIcon provider={provider} />
      )}
      <span>
        {loading ? 'Connecting...' : `Continue with ${config.name}`}
      </span>
    </button>
  )
}
