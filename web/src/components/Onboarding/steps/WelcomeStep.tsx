/**
 * Welcome Step
 *
 * Introduction to the onboarding wizard.
 */

import {
  Sparkles,
  GitBranch,
  Workflow,
  Blocks,
  BookOpen,
  Calendar,
} from "lucide-react";
import { OnboardingModal } from "../OnboardingModal";
import { ReliantIcon } from "../../icons/ReliantIcon";
import type { StepProps } from "../types";

export function WelcomeStep({ stepNumber, totalSteps }: StepProps) {
  return (
    <OnboardingModal
      isOpen={true}
      title="Welcome to Reliant"
      description="AI-powered development workflows"
      stepNumber={stepNumber}
      totalSteps={totalSteps}
      hideNavigation
      hideProgressBar
      icon={<ReliantIcon className="w-12 h-12" />}
    >
      <div className="space-y-4">
        <p className="text-[15px] leading-relaxed text-muted-foreground">
          Reliant orchestrates AI agents to build, test, and review your code —
          all configurable through declarative YAML workflows.
        </p>

        <div className="grid gap-2">
          <FeatureItem
            icon={<Workflow className="w-5 h-5" />}
            title="Multi-Agent Workflows"
            description="Orchestrate specialized agents with loops, branches, parallel execution, and approval gates"
          />
          <FeatureItem
            icon={<GitBranch className="w-5 h-5" />}
            title="Isolated Workspaces"
            description="Work on multiple features simultaneously in isolated git branches"
          />
          <FeatureItem
            icon={<Blocks className="w-5 h-5" />}
            title="Built-in Templates"
            description="Start with pre-built patterns for code review, debugging, testing — or build your own"
          />
        </div>

        <div className="flex items-center justify-center gap-4">
          <a
            href="https://docs.reliantlabs.io/"
            target="_blank"
            rel="noopener noreferrer"
            className="text-sm text-muted-foreground hover:text-primary transition-colors inline-flex items-center gap-1.5"
          >
            <BookOpen className="w-3.5 h-3.5" />
            Read the docs
          </a>
          <span className="text-muted-foreground/50">·</span>
          <a
            href="https://cal.com/team/reliant/onboarding"
            target="_blank"
            rel="noopener noreferrer"
            className="text-sm text-blue-400 hover:text-blue-300 transition-colors inline-flex items-center gap-1.5"
          >
            <Calendar className="w-3.5 h-3.5" />
            Book a demo
          </a>
        </div>

        <div className="flex items-center gap-2 p-2 rounded-lg bg-primary/10 border border-primary/20">
          <Sparkles className="w-4 h-4 text-primary flex-shrink-0" />
          <p className="text-xs text-foreground">
            Skip anytime. Restart from Settings → About.
          </p>
        </div>
      </div>
    </OnboardingModal>
  );
}

function FeatureItem({
  icon,
  title,
  description,
}: {
  icon: React.ReactNode;
  title: string;
  description: string;
}) {
  return (
    <div className="flex items-start gap-3 p-3 rounded-lg bg-muted/50 border-l-2 border-primary/30 hover:bg-muted/70 transition-colors">
      <div className="flex-shrink-0 p-1.5 rounded-lg bg-primary/15 text-primary [&_svg]:w-4 [&_svg]:h-4">
        {icon}
      </div>
      <div className="min-w-0">
        <h4 className="text-sm font-medium text-foreground">{title}</h4>
        <p className="text-xs text-muted-foreground">{description}</p>
      </div>
    </div>
  );
}
