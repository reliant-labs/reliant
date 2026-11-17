package templates

// Configuration owns this gate as well as the runtime: a lockfile may still
// install an older compatible runtime that predates its DNT/GPC defense.
const browserReplayPrivacyGuard = `function replayPrivacyAllows(): boolean {
  try {
    if (typeof navigator === "undefined") return false;
    const nav = navigator as Navigator & { globalPrivacyControl?: boolean; msDoNotTrack?: string };
    const legacy = typeof window === "undefined" ? undefined : (window as Window & { doNotTrack?: string }).doNotTrack;
    return nav.globalPrivacyControl !== true &&
      ![nav.doNotTrack, nav.msDoNotTrack, legacy].some(value => value === "1" || value === "yes");
  } catch {
    return false;
  }
}
`
