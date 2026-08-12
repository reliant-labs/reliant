import { describe, expect, it } from 'vitest'
import {
  capabilitiesFor,
  surfaceForPath,
  type Surface,
  type SurfaceCapabilities,
} from '../surface'

describe('surface capabilities', () => {
  it('gives desktop every capability', () => {
    // Desktop is the full ADE by definition. If a capability is ever added in
    // the "off" position for desktop, that is a mistake in the map rather than
    // a product decision, so assert the invariant directly.
    const caps = capabilitiesFor('desktop')
    for (const [name, enabled] of Object.entries(caps)) {
      expect(enabled, `desktop should enable ${name}`).toBe(true)
    }
  })

  it('keeps authoring off the mobile surface', () => {
    // The central product decision for mobile: monitor and trigger, do not
    // author. These four are the ones most likely to be re-litigated by a
    // reasonable-sounding feature request, so pin them.
    const caps = capabilitiesFor('mobile')
    expect(caps.workflowAuthoring).toBe(false)
    expect(caps.chatWorkflowParams).toBe(false)
    expect(caps.terminal).toBe(false)
    expect(caps.chatExecutionSidebar).toBe(false)
  })

  it('enables the iteration-1 mobile feature set', () => {
    const caps = capabilitiesFor('mobile')
    expect(caps.chatSend).toBe(true)
    expect(caps.chatCreate).toBe(true)
    expect(caps.chatApprovals).toBe(true)
    expect(caps.daemonView).toBe(true)
    expect(caps.daemonResume).toBe(true)
  })

  it('gives mobile a read-only step-list workflow viewer, never authoring', () => {
    // MobileWorkflowStepList/MobileWorkflowScreen — a flat, ordered list with
    // live execution status, reachable from /m/workflows and from a running
    // chat. Not the desktop ReactFlow canvas: that needs pan/zoom room a
    // 390px viewport doesn't have.
    const caps = capabilitiesFor('mobile')
    expect(caps.workflowView).toBe(true)
    expect(caps.workflowAuthoring).toBe(false)
  })

  it('gives mobile a read-only file viewer via the chat drill-in sheet', () => {
    // Browsing and previewing files is now reachable from MobileChatScreen's
    // workspace drill-in — but it's the Prism-based LightweightCodeViewer,
    // never the desktop FileViewerTab/Monaco path.
    const caps = capabilitiesFor('mobile')
    expect(caps.fileViewer).toBe(true)
  })

  it('gives mobile an account screen without giving it settings', () => {
    // These two are deliberately independent axes. `settings` is the ~15
    // section authoring/integration tree; `mobileAccount` is only "who am I
    // and how do I sign out". Collapsing them either strands a phone user in
    // an account they cannot leave, or drags the whole settings shell onto a
    // surface that was never scoped for it.
    const caps = capabilitiesFor('mobile')
    expect(caps.mobileAccount).toBe(true)
    expect(caps.settings).toBe(false)
  })

  it('does not claim capabilities that have no mobile UI', () => {
    // A capability set true with nothing behind it is worse than one set
    // false: it reads as shipped in review, and callers gate on it.
    //
    // `worktreeCreate` — the RPC supports name-only creation but no mobile
    // screen opens that flow.
    // `projectSwitching` — MobileShell picks the user's first project and
    // there is no picker, so a user with three projects can reach only one.
    //
    // Both should flip back to true in the same change that adds the UI.
    const caps = capabilitiesFor('mobile')
    expect(caps.worktreeCreate).toBe(false)
    expect(caps.projectSwitching).toBe(false)
  })

  it('defers the iteration-2 mobile feature set', () => {
    const caps = capabilitiesFor('mobile')
    expect(caps.gitManagement).toBe(false)
    expect(caps.daemonManage).toBe(false)
    expect(caps.worktreeManage).toBe(false)
    expect(caps.chatAttachments).toBe(false)
  })

  it('hides host-owned concerns from the embed surface', () => {
    // The embed is narrower along a different axis than mobile: the host app
    // owns navigation, project context, and settings.
    const caps = capabilitiesFor('embed')
    expect(caps.projectSwitching).toBe(false)
    expect(caps.settings).toBe(false)
    expect(caps.chatCreate).toBe(false)
    // Identity included: a Sign out button inside someone else's app would
    // end a session the host established and never asked us to manage.
    expect(caps.mobileAccount).toBe(false)
    // ...but the widget is still a working chat.
    expect(caps.chatSend).toBe(true)
    expect(caps.chatApprovals).toBe(true)
  })

  it('gives mobile a settings drill-in without giving it full settings', () => {
    // Same independent-axis pattern as mobileAccount: `settings` is the full
    // desktop tree and stays false, but the seven sections shipped for
    // `/m/settings` are individually true.
    const caps = capabilitiesFor('mobile')
    expect(caps.settings).toBe(false)
    expect(caps.settingsAI).toBe(true)
    expect(caps.settingsBilling).toBe(true)
    expect(caps.settingsNotifications).toBe(true)
    expect(caps.settingsPrivacy).toBe(true)
    expect(caps.settingsAppearance).toBe(true)
    expect(caps.settingsWorkspace).toBe(true)
    expect(caps.settingsAbout).toBe(true)
    expect(caps.settingsGitHub).toBe(true)
  })

  it('gives mobile both search modes', () => {
    const caps = capabilitiesFor('mobile')
    expect(caps.searchChats).toBe(true)
    expect(caps.searchFiles).toBe(true)
  })

  it('hides the mobile settings drill-in and search from the embed surface', () => {
    const caps = capabilitiesFor('embed')
    expect(caps.settingsAI).toBe(false)
    expect(caps.settingsBilling).toBe(false)
    expect(caps.settingsNotifications).toBe(false)
    expect(caps.settingsPrivacy).toBe(false)
    expect(caps.settingsAppearance).toBe(false)
    expect(caps.settingsWorkspace).toBe(false)
    expect(caps.settingsAbout).toBe(false)
    expect(caps.settingsGitHub).toBe(false)
    expect(caps.searchChats).toBe(false)
    expect(caps.searchFiles).toBe(false)
  })

  it('scopes embed and mobile by independent policies, not a shared reduced tier', () => {
    // Mobile happens to be a capability superset of embed today — but that's
    // an artifact of where each surface currently stands, not a rule. They
    // got there independently: mobile is narrower because of screen size and
    // input, embed because a host application owns navigation and identity.
    // `workflowView` used to be the clean counterexample (embed had the
    // desktop-style graph, mobile had nothing) before mobile shipped its own
    // read-only step-list viewer; guard against re-deriving one from the
    // other now that they've converged on that axis.
    const mobile = capabilitiesFor('mobile')
    const embed = capabilitiesFor('embed')
    expect(mobile).not.toEqual(embed)
    // Host-owned identity is the one boundary mobile does not share: mobile
    // manages its own session, an embed never does.
    expect(mobile.mobileAccount).toBe(true)
    expect(embed.mobileAccount).toBe(false)
    expect(mobile.chatCreate).toBe(true)
    expect(embed.chatCreate).toBe(false)
  })

  it('declares a position on every capability for every surface', () => {
    // The map's value is that omissions are explicit. A surface missing a key
    // would read as `undefined` (falsy) at call sites and silently disable a
    // feature nobody decided to disable.
    const surfaces: Surface[] = ['desktop', 'mobile', 'embed']
    const keys = Object.keys(
      capabilitiesFor('desktop'),
    ) as (keyof SurfaceCapabilities)[]

    for (const surface of surfaces) {
      const caps = capabilitiesFor(surface)
      for (const key of keys) {
        expect(
          typeof caps[key],
          `${surface} must declare a position on ${key}`,
        ).toBe('boolean')
      }
    }
  })

  describe('surfaceForPath', () => {
    it('routes /m and its descendants to the mobile surface', () => {
      expect(surfaceForPath('/m')).toBe('mobile')
      expect(surfaceForPath('/m/')).toBe('mobile')
      expect(surfaceForPath('/m/chats')).toBe('mobile')
      expect(surfaceForPath('/m/chats/abc-123')).toBe('mobile')
    })

    it('leaves desktop routes alone', () => {
      expect(surfaceForPath('/')).toBe('desktop')
      expect(surfaceForPath('/project/abc')).toBe('desktop')
      expect(surfaceForPath('/workflow/build')).toBe('desktop')
      expect(surfaceForPath('/settings')).toBe('desktop')
    })

    it('does not treat a /m prefix inside another word as mobile', () => {
      // `/migrate` and `/mcp` start with "/m" — a naive startsWith('/m') would
      // hand the desktop app a crippled capability set.
      expect(surfaceForPath('/migrate')).toBe('desktop')
      expect(surfaceForPath('/mcp')).toBe('desktop')
      expect(surfaceForPath('/models')).toBe('desktop')
    })
  })
})
