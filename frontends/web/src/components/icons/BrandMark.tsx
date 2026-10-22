import type { ComponentProps } from "react";
import { ReliantIcon } from "./ReliantIcon";

type BrandMarkProps = Pick<ComponentProps<typeof ReliantIcon>, "className" | "title">;

export function BrandMark({ className = "h-8 w-8", title }: BrandMarkProps) {
  return <ReliantIcon className={className} title={title} />;
}
