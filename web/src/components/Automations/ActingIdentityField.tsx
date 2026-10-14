// Copyright (c) 2025 Reliant Labs

/** Who an automation's integration writes (comments, reviews, pull requests) speak as. */

import { useId } from "react";

import type { ActingIdentityKey } from "@/api/trigger-grpc";
import { hintClass, labelClass } from "../workflow/run/runFormStyles";

const CHOICES: { value: ActingIdentityKey; label: string; hint: string }[] = [
  { value: "owner", label: "You", hint: "Comments, reviews and pull requests appear under your own GitHub account." },
  {
    value: "app",
    label: "Reliant bot",
    hint: "They appear as reliant-labs[bot], signed with this automation's name and yours. Only for repositories you can push to; hosted Reliant only.",
  },
];

export function ActingIdentityField({ value, onChange }: { value: ActingIdentityKey; onChange: (next: ActingIdentityKey) => void }) {
  const ids = useId();
  return (
    <fieldset>
      <legend className={labelClass}>Writes as</legend>
      <div className="space-y-1.5">
        {CHOICES.map((choice) => (
          <label key={choice.value} htmlFor={`${ids}-${choice.value}`} className="flex cursor-pointer items-start gap-2 text-sm">
            <input
              id={`${ids}-${choice.value}`}
              type="radio"
              name={`${ids}-identity`}
              className="mt-1"
              checked={value === choice.value}
              onChange={() => onChange(choice.value)}
            />
            <span>
              <span className="text-foreground">{choice.label}</span>
              <span className={`block ${hintClass}`}>{choice.hint}</span>
            </span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}
