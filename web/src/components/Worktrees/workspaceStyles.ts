export const workspaceButton = {
  primary:
    "rounded-lg border-primary/50 bg-primary text-primary-foreground shadow-sm hover:bg-primary/90 hover:shadow-md focus-visible:ring-primary/60",
  secondary:
    "rounded-lg border-border/80 bg-background text-foreground shadow-sm hover:border-primary/50 hover:bg-muted hover:text-foreground dark:bg-muted/60 dark:hover:bg-muted",
  subtle:
    "rounded-lg border-border/70 bg-card text-foreground shadow-sm hover:border-primary/40 hover:bg-muted hover:text-foreground dark:bg-muted/40 dark:hover:bg-muted",
  warning:
    "rounded-lg border-warning/50 bg-warning/15 text-warning shadow-sm hover:border-warning hover:bg-warning hover:text-warning-foreground focus-visible:ring-warning/60 disabled:hover:border-warning/50 disabled:hover:bg-warning/15 disabled:hover:text-warning",
  destructive:
    "rounded-lg border-destructive/50 bg-destructive/15 text-destructive shadow-sm hover:border-destructive hover:bg-destructive hover:text-destructive-foreground focus-visible:ring-destructive/60",
} as const;

export const workspaceIconButton =
  "inline-flex h-7 w-7 items-center justify-center rounded-lg border border-border/80 bg-background text-foreground shadow-sm transition-colors hover:border-primary/50 hover:bg-muted focus:outline-none focus:ring-2 focus:ring-primary/50";
