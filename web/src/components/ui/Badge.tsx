import React from 'react';
import { cn } from '../../lib/utils';

export interface BadgeProps extends React.HTMLAttributes<HTMLDivElement> {
  variant?: 'default' | 'primary' | 'secondary' | 'success' | 'warning' | 'destructive' | 'outline';
  size?: 'sm' | 'md' | 'lg';
  children: React.ReactNode;
}

const badgeVariants = {
  default: `
    bg-muted text-muted-foreground
    border border-muted
  `,
  primary: `
    bg-primary text-primary-foreground
    border border-primary/20
    shadow-sm
  `,
  secondary: `
    bg-secondary text-secondary-foreground
    border border-secondary/20
  `,
  success: `
    bg-success/10 text-success
    border border-success/20
  `,
  warning: `
    bg-warning/10 text-warning
    border border-warning/20
  `,
  destructive: `
    bg-destructive/10 text-destructive
    border border-destructive/20
  `,
  outline: `
    bg-transparent text-foreground
    border border-border
  `,
};

const badgeSizes = {
  sm: 'px-2 py-0.5 text-xs font-medium',
  md: 'px-2.5 py-1 text-xs font-medium',
  lg: 'px-3 py-1.5 text-sm font-medium',
};

export function Badge({
  className,
  variant = 'default',
  size = 'md',
  children,
  ...props
}: BadgeProps) {
  return (
    <div
      className={cn(
        // Base styles
        'inline-flex items-center rounded-full font-mono',
        'transition-all duration-200 ease-out',
        'hover:scale-105',
        
        // Variant styles
        badgeVariants[variant],
        
        // Size styles
        badgeSizes[size],
        
        className
      )}
      {...props}
    >
      {children}
    </div>
  );
}