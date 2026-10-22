import React from 'react';

interface GradientBackgroundProps {
  /**
   * Additional CSS classes to apply to the container
   */
  className?: string;
  /**
   * Children elements to render on top of the gradient
   */
  children?: React.ReactNode;
  /**
   * Whether to render as absolute positioned overlay (default) or as a container
   * @default 'overlay'
   */
  variant?: 'overlay' | 'container';
}

/**
 * A reusable gradient background component that creates a mesh grid pattern
 * using radial gradients positioned at the four corners.
 *
 * Uses theme-aware primary and secondary colors via CSS custom properties.
 *
 * @example
 * // As an overlay (absolute positioned):
 * <div className="relative">
 *   <GradientBackground />
 *   <YourContent />
 * </div>
 *
 * @example
 * // As a container:
 * <GradientBackground variant="container" className="min-h-screen">
 *   <YourContent />
 * </GradientBackground>
 */
export function GradientBackground({
  className = '',
  children,
  variant = 'overlay'
}: GradientBackgroundProps) {
  const baseClasses = variant === 'overlay'
    ? 'absolute inset-0 overflow-hidden pointer-events-none'
    : 'relative w-full h-full';

  return (
    <div className={`${baseClasses} ${className}`}>
      <div
        className="absolute w-full h-full"
        style={{
          background: `
            radial-gradient(circle at 25% 25%, hsl(var(--primary) / 0.12), transparent 40%),
            radial-gradient(circle at 75% 25%, hsl(var(--secondary) / 0.1), transparent 40%),
            radial-gradient(circle at 25% 75%, hsl(var(--secondary) / 0.08), transparent 40%),
            radial-gradient(circle at 75% 75%, hsl(var(--primary) / 0.1), transparent 40%)
          `,
        }}
      />
      {children && variant === 'container' && (
        <div className="relative z-10">
          {children}
        </div>
      )}
    </div>
  );
}
