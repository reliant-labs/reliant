import React from 'react';
import { cn } from '../../lib/utils';
import { AlertCircle, Check, ExternalLink } from 'lucide-react';

export interface InputProps extends React.InputHTMLAttributes<HTMLInputElement> {
  variant?: 'default' | 'modern' | 'minimal';
  state?: 'default' | 'error' | 'success';
  leftIcon?: React.ReactNode;
  rightIcon?: React.ReactNode;
  label?: string;
  description?: string;
  error?: string;
}

const inputVariants = {
  default: `
    bg-background border border-input
    focus:border-ring focus:ring-2 focus:ring-ring/20
    disabled:opacity-50 disabled:cursor-not-allowed
  `,
  modern: `
    bg-background border border-input
    focus:border-ring focus:ring-2 focus:ring-ring/20 focus:bg-card
    hover:border-border/80
    disabled:opacity-50 disabled:cursor-not-allowed
    shadow-sm focus:shadow-md
    transition-all duration-200 ease-out
  `,
  minimal: `
    bg-transparent border-b border-input border-t-0 border-l-0 border-r-0
    focus:border-ring focus:ring-0 focus:border-b-2
    rounded-none px-0
    disabled:opacity-50 disabled:cursor-not-allowed
  `,
};

const stateStyles = {
  default: '',
  error: 'border-destructive focus:border-destructive focus:ring-destructive/20',
  success: 'border-success focus:border-success focus:ring-success/20',
};

// Helper to render text with clickable URLs
const renderTextWithLinks = (text: string) => {
  const urlRegex = /(https?:\/\/[^\s]+)/g;
  const parts = text.split(urlRegex);
  
  return parts.map((part, index) => {
    if (part.match(urlRegex)) {
      return (
        <a
          key={index}
          href={part}
          target="_blank"
          rel="noopener noreferrer"
          className="text-primary hover:text-primary/80 underline inline-flex items-center gap-0.5"
          onClick={(e) => e.stopPropagation()}
        >
          {part}
          <ExternalLink className="w-2.5 h-2.5" />
        </a>
      );
    }
    return <span key={index}>{part}</span>;
  });
};

export const Input = React.forwardRef<HTMLInputElement, InputProps>(
  ({
    className,
    variant = 'modern',
    state = 'default',
    leftIcon,
    rightIcon,
    label,
    description,
    error,
    type = 'text',
    ...props
  }, ref) => {
    return (
      <div className="w-full">
        {/* Label */}
        {label && (
          <label className="block text-sm font-medium text-foreground mb-2">
            {label}
          </label>
        )}
        
        {/* Input Container */}
        <div className="relative">
          {/* Left Icon */}
          {leftIcon && (
            <div className="absolute left-3 top-1/2 transform -translate-y-1/2 text-muted-foreground">
              {leftIcon}
            </div>
          )}
          
          {/* Input */}
          <input
            type={type}
            className={cn(
              // Base styles
              'flex h-10 w-full rounded-lg px-3 py-2 text-sm',
              'placeholder:text-muted-foreground/60 placeholder:font-normal placeholder:italic',
              'focus:outline-none',
              
              // Left icon padding
              leftIcon && 'pl-10',
              
              // Right icon padding
              (rightIcon || state === 'error' || state === 'success') && 'pr-10',
              
              // Variant styles
              inputVariants[variant],
              
              // State styles
              stateStyles[state],
              
              className
            )}
            ref={ref}
            {...props}
          />
          
          {/* Right Icon or State Icon */}
          <div className="absolute right-3 top-1/2 transform -translate-y-1/2">
            {state === 'error' && <AlertCircle className="w-4 h-4 text-destructive" />}
            {state === 'success' && <Check className="w-4 h-4 text-success" />}
            {state === 'default' && rightIcon && (
              <span className="text-muted-foreground">{rightIcon}</span>
            )}
          </div>
        </div>
        
        {/* Description */}
        {description && !error && (
          <p className="text-xs text-muted-foreground mt-1">{renderTextWithLinks(description)}</p>
        )}
        
        {/* Error Message */}
        {error && (
          <p className="text-xs text-destructive mt-1 flex items-center gap-1">
            <AlertCircle className="w-3 h-3" />
            {error}
          </p>
        )}
      </div>
    );
  }
);

Input.displayName = 'Input';