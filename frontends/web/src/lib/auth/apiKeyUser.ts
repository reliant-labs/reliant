// Copyright (c) 2025 Reliant Labs

/**
 * The synthetic user id of an API-key (machine credential) session
 * (store/authStore setApiKeySession). Such a session has no membership list
 * to choose an org from: the control plane refuses ListMyOrganizations for a
 * machine credential and acts in the credential's own org when a request
 * names none (store/activeOrgStore, lib/orgScope).
 *
 * A module of its own, importing nothing, so the org store can read it
 * without joining the auth store's import cycle.
 */
export const API_KEY_USER_ID = "apikey-user";
