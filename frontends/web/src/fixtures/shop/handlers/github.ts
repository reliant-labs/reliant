// Copyright (c) 2025 Reliant Labs

/**
 * GitHub for the signed-in app: Alex's own GitHub account is connected
 * (GitCredentialService/GetGitCredential), and the Reliant GitHub App is
 * installed on two accounts Alex can see: Acme's GitHub organization, linked
 * to Acme a month ago, and Alex's personal account, linked to nothing
 * (OrganizationService's GitHub App reads). Settings › Organization GitHub
 * reads all three.
 *
 * Part of the app layer only (../app.ts). No avatar URLs: an image off the
 * page's origin would leave fixture mode's fetch boundary.
 */
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { GitCredentialHealth, GitCredentialKind, GitCredentialService } from "@/gen/controlplane/services/git_credential/v1/git_credential_pb";
import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";

import { DAY, DEMO_ORG } from "../shopStory";
import { APP_USER, tsAgo } from "../workspace";
import { serve } from "./serve";

const ACME_INSTALLATION = 51_234_567n;
const PERSONAL_INSTALLATION = 51_234_568n;

/** Only the named methods: the demo pack answers the rest of OrganizationService. */
function only(handlers: MockHandlers, service: { typeName: string }, methods: readonly string[]): MockHandlers {
  const keep = new Set(methods.map((m) => `${service.typeName}/${m}`));
  return Object.fromEntries(Object.entries(handlers).filter(([key]) => keep.has(key)));
}

export function githubScenarioHandlers(): MockHandlers {
  const credential = serve(GitCredentialService, {
    getGitCredential: () => ({
      provider: "github",
      hasToken: true,
      accountLogin: "alex-demo",
      kind: GitCredentialKind.GITHUB_APP,
      health: GitCredentialHealth.VALID,
      createdAt: tsAgo(40 * DAY),
      updatedAt: tsAgo(DAY),
      installations: [
        { accountLogin: "acme", accountType: "Organization", repositorySelection: "all" },
        { accountLogin: "alex-demo", accountType: "User", repositorySelection: "selected" },
      ],
    }),
  });
  const organization = serve(OrganizationService, {
    listGitHubAppInstallations: () => ({
      installations: [
        {
          installationId: ACME_INSTALLATION, orgId: DEMO_ORG, accountLogin: "acme", accountType: "Organization",
          linkedByUserId: APP_USER.id, linkedAt: tsAgo(30 * DAY),
        },
      ],
    }),
    listLinkableGitHubAppInstallations: () => ({
      installations: [
        { installationId: ACME_INSTALLATION, accountLogin: "acme", accountType: "Organization", repositorySelection: "all", linkedHere: true },
        { installationId: PERSONAL_INSTALLATION, accountLogin: "alex-demo", accountType: "User", repositorySelection: "selected" },
      ],
    }),
  });
  return {
    ...only(credential, GitCredentialService, ["GetGitCredential"]),
    ...only(organization, OrganizationService, ["ListGitHubAppInstallations", "ListLinkableGitHubAppInstallations"]),
  };
}
