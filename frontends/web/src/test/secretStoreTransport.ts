/**
 * SecretStoreService at the transport, for tests of code that reaches it
 * through the generated hooks (hooks/controlplane/secret-store-service-hooks_gen.ts):
 * the managed-secret hooks in hooks/forge-queries.ts and the step-up gate.
 *
 *   beforeEach(() => setTransport(mockTransport(secretStoreHandlers({ listSecrets, setSecret }))));
 *   afterEach(() => setTransport(null));
 *
 * Each fake is stated in the UI's terms (services/forge/secretStore.ts: ISO
 * timestamps, author kinds as strings) and called with the arguments the old
 * hand-written wrappers took, so a test keeps its fixtures and its call
 * assertions; this converts the request and the answer at the wire.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  SecretAuthorKind,
  type GetSecretVersionsResponseSchema,
  type ListSecretsResponseSchema,
  type SecretSummarySchema,
  type SecretVersionSchema,
} from "@/gen/controlplane/services/secret_store/v1/secret_store_pb";
import type { MockHandlers } from "@/lib/test-utils";
import type {
  ManagedSecretSummary,
  ManagedSecretVersion,
  SecretVersionAuthor,
  StepUpChallenge,
} from "@/services/forge/secretStore";

type Maybe<T> = T | Promise<T>;
type Summary = Partial<ManagedSecretSummary> & { name: string };
type Version = Partial<ManagedSecretVersion> & { version: number };

export interface SecretStoreFakes {
  listSecrets?: (environmentId: string) => Maybe<{ secrets: Summary[]; protected?: boolean; stepUpValidUntil?: string }>;
  getSecretVersions?: (environmentId: string, name: string) => Maybe<{ summary?: Summary | null; versions?: Version[] }>;
  setSecret?: (args: { environmentId: string; name: string; value: string; cas?: number }) => Maybe<{ version: number; createdAt?: string }>;
  deleteSecret?: (args: { environmentId: string; name: string; versions: number[] }) => Maybe<unknown>;
  undeleteSecret?: (args: { environmentId: string; name: string; versions: number[] }) => Maybe<unknown>;
  destroySecret?: (args: { environmentId: string; name: string; versions: number[] }) => Maybe<unknown>;
  beginStepUp?: (environmentId: string) => Maybe<StepUpChallenge>;
  completeStepUp?: (environmentId: string, code: string) => Maybe<{ validUntil?: string }>;
}

const ts = (iso: string | undefined) => (iso ? timestampFromDate(new Date(iso)) : undefined);

const AUTHOR_KIND: Record<SecretVersionAuthor["kind"], SecretAuthorKind> = {
  user: SecretAuthorKind.USER,
  token: SecretAuthorKind.TOKEN,
  platform: SecretAuthorKind.PLATFORM,
};

export function summaryToWire(s: Summary): MessageInitShape<typeof SecretSummarySchema> {
  return {
    name: s.name,
    currentVersion: s.currentVersion ?? 0,
    oldestVersion: s.oldestVersion ?? 0,
    maxVersions: s.maxVersions ?? 0,
    createdTime: ts(s.createdAt),
    updatedTime: ts(s.updatedAt),
    currentVersionDeleted: s.currentVersionDeleted ?? false,
    currentVersionDestroyed: s.currentVersionDestroyed ?? false,
  };
}

function versionToWire(v: Version): MessageInitShape<typeof SecretVersionSchema> {
  return {
    version: v.version,
    createdTime: ts(v.createdAt),
    deletionTime: ts(v.deletedAt),
    destroyed: v.destroyed ?? false,
    setBy: v.setBy ? { kind: AUTHOR_KIND[v.setBy.kind], email: v.setBy.email ?? "" } : undefined,
  };
}

/** Handlers for SecretStoreService from UI-shaped fakes; an rpc with no fake is not answered. */
export function secretStoreHandlers(fakes: SecretStoreFakes): MockHandlers {
  const handlers: MockHandlers = {};
  const on = (rpc: string, handler: (req: never) => unknown) => {
    handlers[`SecretStoreService.${rpc}`] = handler as (req: unknown) => unknown;
  };
  const versioned = (fake: (args: { environmentId: string; name: string; versions: number[] }) => Maybe<unknown>) =>
    async (req: { environmentId: string; name: string; versions: number[] }) => {
      await fake({ environmentId: req.environmentId, name: req.name, versions: [...req.versions] });
      return {};
    };

  const { listSecrets, getSecretVersions, setSecret, deleteSecret, undeleteSecret, destroySecret, beginStepUp, completeStepUp } = fakes;
  if (listSecrets) {
    on("ListSecrets", async (req: { environmentId: string }): Promise<MessageInitShape<typeof ListSecretsResponseSchema>> => {
      const r = await listSecrets(req.environmentId);
      return { secrets: r.secrets.map(summaryToWire), protected: r.protected ?? false, stepUpValidUntil: ts(r.stepUpValidUntil) };
    });
  }
  if (getSecretVersions) {
    on("GetSecretVersions", async (req: { environmentId: string; name: string }): Promise<MessageInitShape<typeof GetSecretVersionsResponseSchema>> => {
      const r = await getSecretVersions(req.environmentId, req.name);
      return { summary: r.summary ? summaryToWire(r.summary) : undefined, versions: (r.versions ?? []).map(versionToWire) };
    });
  }
  if (setSecret) {
    on("SetSecret", async (req: { environmentId: string; name: string; secretValue: string; cas?: number }) => {
      const r = await setSecret({ environmentId: req.environmentId, name: req.name, value: req.secretValue, cas: req.cas });
      return { version: r.version, createdTime: ts(r.createdAt) };
    });
  }
  if (deleteSecret) on("DeleteSecret", versioned(deleteSecret));
  if (undeleteSecret) on("UndeleteSecret", versioned(undeleteSecret));
  if (destroySecret) on("DestroySecret", versioned(destroySecret));
  if (beginStepUp) {
    on("BeginStepUp", async (req: { environmentId: string }) => {
      const r = await beginStepUp(req.environmentId);
      return { sentTo: r.sentTo, expiresAt: ts(r.expiresAt) };
    });
  }
  if (completeStepUp) {
    on("CompleteStepUp", async (req: { environmentId: string; code: string }) => {
      const r = await completeStepUp(req.environmentId, req.code);
      return { validUntil: ts(r.validUntil) };
    });
  }
  return handlers;
}
