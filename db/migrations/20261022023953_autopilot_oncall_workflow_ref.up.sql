-- forge:breaks-rollback — the built-in Autopilot alert-triage workflow is now autopilot-oncall, and activations are re-pointed at it here. The previous release has no workflow of that name, so it cannot resolve these activations (their runs fail to start) until it is rolled forward again.

-- Migration: autopilot_oncall_workflow_ref
-- Created: 2026-10-22T02:39:53Z
--
-- The built-in `autopilot-alert-triage` became `autopilot-oncall` ("Autopilot · on-call"): one workflow for
-- alerts and for the platform's issue alerts. An activation names its workflow by ref, so every activation of
-- the old one is moved to the new one; its trigger (`alertmanager`), params, token and control-plane receiver
-- are unchanged, so an enabled activation keeps receiving alerts.

UPDATE public.triggers
   SET workflow = CASE workflow
                    WHEN 'builtin://autopilot-alert-triage' THEN 'builtin://autopilot-oncall'
                    ELSE 'autopilot-oncall'
                  END,
       updated_at = NOW()
 WHERE workflow IN ('builtin://autopilot-alert-triage', 'autopilot-alert-triage');
