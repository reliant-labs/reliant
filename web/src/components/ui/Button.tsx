import React from 'react';
import { cn } from '../../lib/utils';
import { Loader2 } from 'lucide-react';

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'default' | 'primary' | 'secondary' | 'accent' | 'destructive' | 'ghost' | 'outline' | 'gradient';
  size?: 'xs' | 'sm' | 'md' | 'lg' | 'xl';
  loading?: boolean;
  leftIcon?: React.ReactNode;
  rightIcon?: React.ReactNode;
  children: React.ReactNode;
}

const buttonVariants = {
  default: `
    bg-primary text-primary-foreground hover:bg-primary/90
    border border-primary/20
    elevation-1 hover:elevation-2
    focus:ring-2 focus:ring-primary/50 focus:ring-offset-2
    active:scale-[0.98] active:elevation-1 transition-all duration-150 ease-out
  `,
  primary: `
    bg-primary text-primary-foreground hover:bg-primary/90
    border border-primary/20
    elevation-1 hover:elevation-2
    focus:ring-2 focus:ring-primary/50 focus:ring-offset-2
    active:scale-[0.98] active:elevation-1 transition-all duration-150 ease-out
  `,
  secondary: `
    bg-secondary text-secondary-foreground hover:bg-secondary/80
    border border-border/50
    elevation-1 hover:elevation-2
    focus:ring-2 focus:ring-border focus:ring-offset-2
    active:scale-[0.98] active:elevation-1 transition-all duration-150 ease-out
  `,
  accent: `
    bg-accent text-accent-foreground hover:bg-accent/80
    border border-accent/20
    elevation-1 hover:elevation-2
    focus:ring-2 focus:ring-accent/50 focus:ring-offset-2
    active:scale-[0.98] active:elevation-1 transition-all duration-150 ease-out
  `,
  destructive: `
    bg-destructive/10 text-destructive hover:bg-destructive/20
    border border-destructive/20
    elevation-1 hover:elevation-2
    focus:ring-2 focus:ring-destructive/50 focus:ring-offset-2
    active:scale-[0.98] active:elevation-1 transition-all duration-150 ease-out
  `,
  ghost: `
    hover:bg-accent hover:text-accent-foreground
    text-muted-foreground
    focus:ring-2 focus:ring-accent/50 focus:ring-offset-2
    active:scale-[0.98] transition-all duration-150 ease-out
  `,
  outline: `
    border border-input bg-background hover:bg-accent hover:text-accent-foreground
    text-foreground
    hover:elevation-1
    focus:ring-2 focus:ring-accent/50 focus:ring-offset-2
    active:scale-[0.98] transition-all duration-150 ease-out
  `,
  gradient: `
    bg-gradient-to-r from-primary to-primary hover:from-primary/90 hover:to-primary/90
    text-primary-foreground
    border border-primary/20
    elevation-2 hover:elevation-3
    focus:ring-2 focus:ring-primary/50 focus:ring-offset-2
    active:scale-[0.98] active:elevation-1 transition-all duration-150 ease-out
  `,
};

const buttonSizes = {
  xs: 'h-6 px-2 text-xs font-medium',
  sm: 'h-7 px-2.5 text-xs font-medium',
  md: 'h-8 px-3 text-sm font-medium',
  lg: 'h-9 px-4 text-sm font-medium',
  xl: 'h-10 px-5 text-base font-medium',
};

export function Button({
  className,
  variant = 'primary',
  size = 'md',
  loading = false,
  leftIcon,
  rightIcon,
  disabled,
  children,
  style,
  ...props
}: ButtonProps) {
  // Inline styles for variants that use primary/secondary colors
  // This is needed because Tailwind utility classes aren't resolving CSS custom properties correctly
  const getVariantStyles = () => {
    switch (variant) {
      case 'default':
      case 'primary':
        return {
          backgroundColor: 'hsl(var(--primary))',
          color: 'hsl(var(--primary-foreground))',
          borderColor: 'hsl(var(--primary) / 0.2)',
        };
      case 'secondary':
        return {
          backgroundColor: 'hsl(var(--secondary))',
          color: 'hsl(var(--secondary-foreground))',
          borderColor: 'hsl(var(--border) / 0.5)',
        };
      default:
        return undefined;
    }
  };

  return (
    <button
      className={cn(
        // Base styles
        'inline-flex items-center justify-center gap-1.5 rounded-md font-medium',
        'focus:outline-none',
        'disabled:opacity-50 disabled:cursor-not-allowed disabled:transform-none disabled:shadow-none',
        'cursor-pointer',

        // Variant styles (keep for non-color properties like hover effects)
        buttonVariants[variant],

        // Class for visible hover when variant uses inline background
        (variant === 'primary' || variant === 'default') && 'btn-primary',

        // Size styles
        buttonSizes[size],

        className
      )}
      style={{ ...getVariantStyles(), ...style }}
      disabled={disabled || loading}
      {...props}
    >
      {loading && <Loader2 className="w-4 h-4 animate-spin" />}
      {!loading && leftIcon && <span className="flex-shrink-0">{leftIcon}</span>}
      <span className="truncate">{children}</span>
      {!loading && rightIcon && <span className="flex-shrink-0">{rightIcon}</span>}
    </button>
  );
}
