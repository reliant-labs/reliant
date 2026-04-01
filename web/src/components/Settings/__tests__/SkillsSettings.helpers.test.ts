import { describe, expect, it } from "vitest";
import {
  buildSkillAvatarContainerClassName,
  getSkillInitials,
  isPackagedAssetPath,
  pickBestAssetPath,
} from "../SkillsSettings";

describe("isPackagedAssetPath", () => {
  it("matches assets paths without a leading slash", () => {
    expect(isPackagedAssetPath("assets/icon.png")).toBe(true);
    expect(isPackagedAssetPath("skills/linear/assets/logo.svg")).toBe(true);
  });

  it("matches assets paths with a leading slash", () => {
    expect(isPackagedAssetPath("/assets/icon.png")).toBe(true);
  });

  it("rejects non-assets image paths", () => {
    expect(isPackagedAssetPath("icon.png")).toBe(false);
    expect(isPackagedAssetPath("images/icon.png")).toBe(false);
  });
});

describe("buildSkillAvatarContainerClassName", () => {
  it("uses transparent background when rendering image icons", () => {
    const className = buildSkillAvatarContainerClassName({
      hasImageIcon: true,
      colorClass: "from-sky-500/30 to-blue-500/20",
    });

    expect(className).toContain("bg-transparent");
    expect(className).not.toContain("bg-gradient-to-br");
  });

  it("uses gradient background when rendering fallback icons", () => {
    const className = buildSkillAvatarContainerClassName({
      hasImageIcon: false,
      colorClass: "from-sky-500/30 to-blue-500/20",
    });

    expect(className).toContain("bg-gradient-to-br");
    expect(className).toContain("from-sky-500/30 to-blue-500/20");
    expect(className).toContain("text-foreground/90");
  });
});

describe("getSkillInitials", () => {
  it("uses first letter of up to two words", () => {
    expect(getSkillInitials("Zen MCP")).toBe("ZM");
    expect(getSkillInitials("Fetch")).toBe("F");
    expect(getSkillInitials("")).toBe("S");
  });
});

describe("pickBestAssetPath", () => {
  it("prefers png over svg when both are available", () => {
    const best = pickBestAssetPath(["assets/icon.svg", "assets/icon.png"]);
    expect(best).toBe("assets/icon.png");
  });

  it("prefers svg over other raster formats except png", () => {
    const best = pickBestAssetPath(["assets/icon.jpg", "assets/icon.svg"]);
    expect(best).toBe("assets/icon.svg");
  });
});
