/**
 * shopfront, U6 Auth: the sign-in service Reliant runs for prod (a managed identity) and its people.
 *
 *   Status  prod's identity is ready, with the storefront's two applications and one delivery of its
 *           outputs; the caller is an org admin; staging declares none.
 *   Users   six people across active, invited and locked, with the declared roles (admin, editor,
 *           viewer), and a sign-in time for those who have signed in.
 *
 * Fabricated; every time is relative to FIXED_NOW.
 */
import type { Fixtures } from '../rpc'
import { DAY, HOUR, MIN, iso } from './base'

const PROD = 'env-shop-prod'

const ROLES = [
  { key: 'admin', displayName: 'Admin', group: 'shopfront', declared: true },
  { key: 'editor', displayName: 'Editor', group: 'shopfront', declared: true },
  { key: 'viewer', displayName: 'Viewer', group: 'shopfront', declared: true },
]

const user = (userId: string, email: string, displayName: string, state: string, roles: string[], created: number, signedIn?: number) => ({
  userId, email, displayName, state: `IDENTITY_USER_STATE_${state}`, roles, createdAt: iso(created), ...(signedIn === undefined ? {} : { lastSignInAt: iso(signedIn) }),
})

const USERS = [
  user('u-1', 'alex@example.com', 'Alex Rivera', 'ACTIVE', ['admin'], 30 * DAY, 12 * MIN),
  user('u-2', 'priya@example.com', 'Priya Nair', 'ACTIVE', ['admin', 'editor'], 26 * DAY, 3 * HOUR),
  user('u-3', 'tomas@example.com', 'Tomas Berg', 'ACTIVE', ['editor'], 19 * DAY, 2 * DAY),
  user('u-4', 'mei@example.com', 'Mei Tanaka', 'ACTIVE', ['viewer'], 11 * DAY, 6 * DAY),
  user('u-5', 'sam@example.com', 'Sam Okoye', 'INVITED', ['viewer'], 2 * DAY),
  user('u-6', 'jo@example.com', 'Jo Lindgren', 'LOCKED', ['viewer'], 40 * DAY, 21 * DAY),
]

const status = (req: Record<string, unknown>) => {
  if (req.environmentId !== PROD) {
    return { hasInstance: false, notEnabledReason: 'IDENTITY_NOT_ENABLED_REASON_NOT_DECLARED', notEnabledMessage: 'This environment declares no identity.' }
  }
  return {
    hasInstance: true, state: 'IDENTITY_STATE_READY', issuer: 'https://shopfront-prod.id.reliant.app', domain: 'shopfront-prod.id.reliant.app',
    apps: [
      { name: 'storefront', clientId: '281904738120311841@shopfront', brokerExpiresAt: iso(-60 * DAY) },
      { name: 'admin', clientId: '281904738120377377@shopfront', brokerExpiresAt: iso(-45 * DAY) },
    ],
    deliveries: [{ kind: 'KUBERNETES_SECRET', cluster: 'prod', namespace: 'shopfront-prod', name: 'shopfront-identity', state: 'IDENTITY_DELIVERY_STATE_DELIVERED', lastVerifiedAt: iso(4 * MIN), lastWrittenAt: iso(9 * DAY) }],
    ownerEnvironmentId: PROD, sharerEnvironmentIds: [], lastReconciledAt: iso(4 * MIN), failures: 0,
    exportUrlAvailable: false, callerCanManage: true, exportEnabled: true, mailDelivery: 'IDENTITY_MAIL_DELIVERY_SMTP',
    ownerEnvironment: { id: PROD, name: 'prod', project: 'shopfront' }, sharerEnvironments: [],
  }
}

export const identityFixtures: Fixtures = {
  'controlplane.v1.IdentityService/GetIdentityStatus': status,
  'controlplane.v1.IdentityService/ListUsers': (req) => (req.environmentId === PROD ? { users: USERS, roles: ROLES, totalSize: USERS.length, nextPageToken: '' } : {}),
  'controlplane.v1.IdentityService/GetUser': (req) => ({ user: USERS.find((u) => u.userId === req.userId) }),
}
