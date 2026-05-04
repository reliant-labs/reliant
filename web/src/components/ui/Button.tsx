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
    shadow-sm shadow-primary/20 hover:shadow-md hover:shadow-primary/25
    focus:ring-2 focus:ring-primary/50 focus:ring-offset-2
    active:scale-[0.98] active:shadow-sm transition-all duration-150 ease-out
  `,
  primary: `
    bg-primary text-primary-foreground hover:bg-primary/90
    border border-primary/20
    shadow-sm shadow-primary/20 hover:shadow-md hover:shadow-primary/25
    focus:ring-2 focus:ring-primary/50 focus:ring-offset-2
    active:scale-[0.98] active:shadow-sm transition-all duration-150 ease-out
  `,
  secondary: `
    bg-secondary text-secondary-foreground hover:bg-secondary/80
    border border-border/50
    shadow-sm hover:shadow-md
    focus:ring-2 focus:ring-border focus:ring-offset-2
    active:scale-[0.98] active:shadow-sm transition-all duration-150 ease-out
  `,
  accent: `
    bg-accent text-accent-foreground hover:bg-accent/80
    border border-accent/20
    shadow-sm shadow-accent/20 hover:shadow-md hover:shadow-accent/25
    focus:ring-2 focus:ring-accent/50 focus:ring-offset-2
    active:scale-[0.98] active:shadow-sm transition-all duration-150 ease-out
  `,
  destructive: `
    bg-destructive/10 text-destructive hover:bg-destructive/20
    border border-destructive/20
    shadow-sm shadow-destructive/10 hover:shadow-md hover:shadow-destructive/15
    focus:ring-2 focus:ring-destructive/50 focus:ring-offset-2
    active:scale-[0.98] active:shadow-sm transition-all duration-150 ease-out
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
    shadow-md shadow-primary/25 hover:shadow-lg hover:shadow-primary/30
    focus:ring-2 focus:ring-primary/50 focus:ring-offset-2
    active:scale-[0.98] active:shadow-sm transition-all duration-150 ease-out
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
  return (
    <button
      className={cn(
        // Base styles
        'inline-flex items-center justify-center gap-1.5 rounded-md font-medium',
        'focus:outline-none',
        'disabled:opacity-50 disabled:cursor-not-allowed disabled:transform-none disabled:shadow-none',
        'cursor-pointer',

        // Variant styles
        buttonVariants[variant],

        // Size styles
        buttonSizes[size],

        className
      )}
      style={style}
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
