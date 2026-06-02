import { useEffect, useState } from "react";
import { ExternalLink, BookOpen, FileText, Shield, Globe, Terminal, Calendar, Github, Slack, WandSparkles } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "../../lib/toast-manager";
import { UpdateSection } from "./UpdateSection";
import { systemGrpc } from "../../api/system-grpc";
import { useTourStore } from "../../store/tourStore";
import { ONBOARDING_STEPS } from "../Onboarding/constants";
import { BrandMark } from "../icons/BrandMark";

type LinkItem = {
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  href?: string;
  onClick?: () => void;
};

interface VersionInfo {
  version: string;
  commit: string;
  date: string;
  branch: string;
}

export function AboutSection() {
  const [versionInfo, setVersionInfo] = useState<VersionInfo | null>(null);
  const [isInstallingCLI, setIsInstallingCLI] = useState(false);
  const isElectron = !!window.electronAPI;
  const navigate = useNavigate();

  const handleInstallCLI = async () => {
    if (!window.electronAPI?.installCLI) return;

    setIsInstallingCLI(true);
    try {
      const result = await window.electronAPI.installCLI();
      if (result.success) {
        toast.success("CLI installed! You can now use 'reliant' command in your terminal.");
      } else {
        toast.error(result.error || "Failed to install CLI");
      }
    } catch (error) {
      toast.error("Failed to install CLI. You may need to run with sudo.");
    } finally {
      setIsInstallingCLI(false);
    }
  };

  useEffect(() => {
    const fetchVersionInfo = async () => {
      try {
        const data = await systemGrpc.version();
        setVersionInfo({
          version: data.version,
          commit: data.commit,
          date: data.date,
          branch: data.branch,
        });
      } catch (error) {
        console.error("Failed to fetch version info:", error);
        setVersionInfo({
          version: "1.0.0",
          commit: "unknown",
          date: new Date().toISOString().split("T")[0],
          branch: "main",
        });
      }
    };

    fetchVersionInfo();
  }, []);

  const links: LinkItem[] = [
    {
      icon: BookOpen,
      label: "Docs",
      href: "https://docs.reliantlabs.io/",
    },
    {
      icon: Globe,
      label: "Website",
      href: "https://reliantlabs.io",
    },
    {
      icon: Calendar,
      label: "Book a demo",
      href: "https://cal.com/team/reliant/onboarding",
    },
    {
      icon: Github,
      label: "GitHub",
      href: "https://github.com/reliant-labs/reliant",
    },
    {
      icon: Slack,
      label: "Join Slack",
      href: "https://join.slack.com/t/reliant-pn51441/shared_invite/zt-3g6mhfnhx-~CWMzNRZUylWHevlJXO89A",
    },
    {
      icon: FileText,
      label: "Terms of Service",
      href: "https://reliantlabs.io/terms",
    },
    {
      icon: Shield,
      label: "Privacy Policy",
      href: "https://reliantlabs.io/privacy",
    },
    {
      icon: WandSparkles,
      label: "Restart Onboarding Guide",
      onClick: () => {
        // Drop the user on the home route with `?tour=<first-step>` — the
        // wizard reads the URL and takes it from there. The reset is
        // fire-and-forget (matches OnboardingChecklist.startTour); waiting on
        // the persistence RPC just delays the navigation and, when the RPC
        // is slow, makes the click look like a no-op.
        void useTourStore.getState().resetTourProgress();
        void navigate({
          to: "/",
          search: { tour: ONBOARDING_STEPS[0].id },
        });
        toast.success("Onboarding guide has been reset");
      },
    },
    // Only show CLI install option in Electron
    ...(isElectron ? [{
      icon: Terminal,
      label: isInstallingCLI ? "Installing..." : "Install CLI Command",
      onClick: handleInstallCLI,
    }] : []),
  ];

  return (
    <div className="h-full w-full flex items-center justify-center text-muted-foreground chat-background relative overflow-y-auto py-12">
      <div className="absolute inset-0 backdrop-blur-sm bg-background/30" />
      
      <div className="relative z-10 w-full max-w-sm px-6" data-onboarding="about-settings">
        {/* Logo & Brand */}
        <div className="text-center mb-10">
          <BrandMark className="w-24 h-24 mx-auto mb-4" />
          <h1 className="text-3xl font-semibold text-foreground tracking-tight">
            Reliant
          </h1>
          <p className="text-sm text-muted-foreground mt-1">
            v{versionInfo?.version || "1.0.0"}
          </p>
        </div>

        {/* Quick Links */}
        <div className="bg-card/50 backdrop-blur-sm rounded-xl border border-border/50 divide-y divide-border/50 mb-6">
          {links.map((link) => {
            const content = (
              <>
                <span className="flex items-center gap-3">
                  <link.icon className="w-4 h-4 text-muted-foreground group-hover:text-primary transition-colors" />
                  {link.label}
                </span>
                {link.href && (
                  <ExternalLink className="w-3.5 h-3.5 text-muted-foreground/50 group-hover:text-muted-foreground transition-colors" />
                )}
              </>
            );

            if (link.onClick) {
              return (
                <a
                  key={link.label}
                  href="#"
                  onClick={(e) => {
                    e.preventDefault();
                    link.onClick?.();
                  }}
                  className="flex items-center justify-between px-4 py-3 text-sm text-foreground/80 hover:text-foreground hover:bg-muted/50 transition-colors first:rounded-t-xl last:rounded-b-xl group"
                >
                  {content}
                </a>
              );
            }

            return (
              <a
                key={link.href}
                href={link.href}
                target="_blank"
                rel="noopener noreferrer"
                className="flex items-center justify-between px-4 py-3 text-sm text-foreground/80 hover:text-foreground hover:bg-muted/50 transition-colors first:rounded-t-xl last:rounded-b-xl group"
              >
                {content}
              </a>
            );
          })}
        </div>

        {/* Update Section */}
        <UpdateSection />

        {/* Copyright */}
        <p className="text-center text-xs text-muted-foreground/60 mt-8">
          © {new Date().getFullYear()} Reliant Labs
        </p>
      </div>
    </div>
  );
}