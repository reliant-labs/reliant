import { render } from "@testing-library/react";
import { SocialProviderIcon, type SocialProvider } from "./SocialProviderIcon";

it.each<SocialProvider>(["google", "github", "apple"])(
  "renders %s as a decorative icon",
  (provider) => {
    const { container } = render(<SocialProviderIcon provider={provider} />);

    const svg = container.querySelector("svg");
    expect(svg).toHaveAttribute("aria-hidden", "true");
    expect(svg).toHaveAttribute("focusable", "false");
  },
);
