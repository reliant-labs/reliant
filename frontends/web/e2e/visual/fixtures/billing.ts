/**
 * Usage and billing fixtures for the subscribed (Compute M) user: 62 of 160
 * machine hours used, observability at 6.4 of 10 GB. Ported from the November
 * review's fixtures-onboarding-billing/20-usage.js and 30-org-obs-hosting.js.
 */
import type { Fixtures } from '../rpc'
import { FIXED_NOW, HOUR, MIN, PERIOD, iso, isoAhead } from './base'

const GIB = 1073741824
const now = new Date(FIXED_NOW)
const monthStart = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1)).toISOString()
const monthEnd = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() + 1, 1)).toISOString()

export const billingFixtures: Fixtures = {
  'controlplane.v1.BillingService/GetCurrentUserComputeUsage': {
    usageMeasured: true, periodStart: PERIOD.start, periodEnd: PERIOD.end, measuredFrom: PERIOD.start, measuredThrough: iso(5 * MIN),
    pricingVersion: 'machine-pooled-v1',
    allowanceWeightedSeconds: '37221280848000/41831851',
    consumedWeightedSeconds: '14423246328600/41831851',
    remainingWeightedSeconds: '22798034519400/41831851',
    grantRemainingWeightedSeconds: '0',
    runningCostWeightSum: '258481117/167327404',
    exhaustsAt: isoAhead(98 * HOUR),
    bySize: [{
      size: 'm', exactRunningSeconds: String(62 * 3600),
      costWeight: '258481117/167327404', includedWeightedSeconds: '14423246328600/41831851',
      exactOverageSeconds: '0', overageCostCents: '0', pricingVersion: 'machine-pooled-v1',
    }],
    storage: {
      provisionedGib: '50', includedGib: '50', extraGib: '0', exactExtraGibSeconds: '0',
      costCents: '0', projectedCostCents: '0', centsPerGibMonth: '25',
    },
    projectedPeriodTotalCents: '3900', planPriceCents: '3900', estimatedOverageCostCents: 0,
  },
  'observability_billing.v1.ObservabilityBillingService/GetObservabilityUsage': {
    periodStart: monthStart, periodEnd: monthEnd, measuredThrough: iso(10 * MIN),
    usedBytes: String(Math.round(6.4 * GIB)), allowanceBytes: String(10 * GIB), overageCents: '0',
    projectedBytes: String(Math.round(6.4 * 1.9 * GIB)), projectedOverageCents: '54', rateCentsPerGb: '25.0000',
    state: 'OBSERVABILITY_QUOTA_STATE_OK', usageState: 'OBSERVABILITY_QUOTA_STATE_OK', funded: true, exempt: false,
    spendCapCents: '5000', spendCapReached: false, browserBytes: String(Math.round(0.9 * GIB)),
    browserBilledBytes: String(Math.round(0.9 * GIB)), browserCapBytes: String(2 * GIB), bySignal: [], byService: [],
    operatorBytes: '0', metadataBytes: String(Math.round(0.2 * GIB)), chargedCents: '0', unchargedBytes: '0',
  },
}
