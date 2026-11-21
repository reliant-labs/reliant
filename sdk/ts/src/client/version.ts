// The SDK's identity on the wire: every call carries
// `Reliant-Client: sdk-ts/<SDK_VERSION>`, which the server compares with its
// minimum for this SDK and refuses with `upgrade_required` when it is older.

/**
 * This package's version, the `version` in its package.json
 * (published-surface.test.ts fails when the two differ, so a release that
 * bumps one bumps both).
 */
export const SDK_VERSION = "0.1.0";

/** The request header that names the client and its version. */
export const RELIANT_CLIENT_HEADER = "Reliant-Client";

/** The value of {@link RELIANT_CLIENT_HEADER} this SDK sends. */
export const SDK_CLIENT_ID = `sdk-ts/${SDK_VERSION}`;
