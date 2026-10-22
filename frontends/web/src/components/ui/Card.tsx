import React from 'react';
import { cn } from '../../lib/utils';

export interface CardProps extends React.HTMLAttributes<HTMLDivElement> {
  variant?: 'default' | 'elevated' | 'outlined' | 'glass';
  size?: 'sm' | 'md' | 'lg';
  hover?: boolean;
  children: React.ReactNode;
}

const cardVariants = {
  default: `
    elevation-1
    text-card-foreground
    border border-border/40
  `,
  elevated: `
    elevation-2
    text-card-foreground
    border border-border/40
  `,
  outlined: `
    bg-background text-foreground
    border-2 border-border
    shadow-none
  `,
  glass: `
    bg-card/70 text-card-foreground
    border border-border/30
    backdrop-blur-md
    elevation-2
  `,
};

const cardSizes = {
  sm: 'p-3 rounded-lg',
  md: 'p-4 rounded-xl',
  lg: 'p-6 rounded-xl',
};

export function Card({
  className,
  variant = 'default',
  size = 'md',
  hover = true,
  children,
  ...props
}: CardProps) {
  return (
    <div
      className={cn(
        // Base styles
        'transition-all duration-200 ease-out',
        
        // Hover effects
        hover && variant === 'elevated' && 'hover:elevation-3 hover:scale-[1.01] hover:translate-y-[-2px]',
        hover && variant === 'default' && 'hover:elevation-2',
        
        // Variant styles
        cardVariants[variant],
        
        // Size styles
        cardSizes[size],
        
        className
      )}
      {...props}
    >
      {children}
    </div>
  );
}

// Card composition components
export function CardHeader({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        'flex flex-col space-y-1.5 pb-4',
        className
      )}
      {...props}
    >
      {children}
    </div>
  );
}

export function CardTitle({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLHeadingElement>) {
  return (
    <h3
      className={cn(
        'text-lg font-semibold leading-none tracking-tight',
        'bg-gradient-to-r from-foreground to-foreground/80 bg-clip-text text-transparent',
        className
      )}
      {...props}
    >
      {children}
    </h3>
  );
}

export function CardDescription({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLParagraphElement>) {
  return (
    <p
      className={cn(
        'text-sm text-muted-foreground leading-relaxed',
        className
      )}
      {...props}
    >
      {children}
    </p>
  );
}

export function CardContent({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn('flex-1', className)}
      {...props}
    >
      {children}
    </div>
  );
}

export function CardFooter({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        'flex items-center pt-4 border-t border-border/50',
        className
      )}
      {...props}
    >
      {children}
    </div>
  );
}