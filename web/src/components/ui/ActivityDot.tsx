import React from "react";
import { cn } from "../../lib/utils";
import { Tooltip } from "./Tooltip";

// Chat activity states (shared between Sidebar and WorkflowHub)
export type ChatActivityState =
  | "idle"
  | "thinking"
  | "streaming"
  | "awaiting_approval"
  | "background_running"
  | "error";

interface ActivityDotProps {
  state: ChatActivityState;
  label?: string; // Override the default tooltip label
  size?: "sm" | "md"; // sm = 2, md = 2.5 (default)
  placement?: "left" | "right" | "top" | "bottom";
}

export const ActivityDot: React.FC<ActivityDotProps> = ({
  state,
  label,
  size = "md",
  placement = "left",
}) => {
  const getDotConfig = () => {
    switch (state) {
      case "thinking":
        return {
          color: "bg-green-500",
          animation: "animate-pulse",
          defaultLabel: "AI thinking",
        };
      case "streaming":
        return {
          color: "bg-green-500",
          animation: "animate-pulse",
          defaultLabel: "Streaming response",
        };
      case "awaiting_approval":
        return {
          color: "bg-green-500",
          animation: "animate-pulse",
          defaultLabel: "Approval needed",
        };
      case "background_running":
        return {
          color: "bg-green-500",
          animation: "animate-pulse",
          defaultLabel: "Commands running",
        };
      case "error":
        return {
          color: "bg-destructive",
          animation: "animate-pulse",
          defaultLabel: "Error occurred",
        };
      default:
        return {
          color: "bg-muted-foreground",
          animation: "",
          defaultLabel: "Idle",
        };
    }
  };

  const config = getDotConfig();
  const sizeClass = size === "sm" ? "w-2 h-2" : "w-2.5 h-2.5";

  return (
    <Tooltip content={label || config.defaultLabel} placement={placement} delay={300}>
      <div
        className={cn(
          "rounded-full",
          sizeClass,
          config.color,
          config.animation
        )}
      />
    </Tooltip>
  );
};
