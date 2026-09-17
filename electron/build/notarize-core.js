// notarize-core.js — the single implementation of macOS notarization.
//
// Both electron/build/notarize.js and electron/build/notarize-safe.js are thin
// wrappers over this module. They used to be near-duplicate copies, which is
// how the 403 "agreement missing" classification ended up fixed in neither.
//
// ── Credentials ───────────────────────────────────────────────────────────
//
// Two auth strategies, tried in this order:
//
//   1. App Store Connect API key (PREFERRED)
//        APPLE_API_KEY      — the .p8 private key: either a filesystem path,
//                             or the key's literal CONTENTS (see below)
//        APPLE_API_KEY_ID   — e.g. T9GPZ92M7K
//        APPLE_API_ISSUER   — UUID, e.g. c055ca8c-e5a8-4836-b61d-aa5794eeb3f4
//
//   2. Apple ID + app-specific password (FALLBACK)
//        APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD, APPLE_TEAM_ID
//
// The API key is preferred because it belongs to the TEAM, not to a person.
// v1.7.14 shipped Windows and Linux but not macOS: the Apple account holder
// changed, code signing kept working (it uses the certificate), and
// notarization began failing with "HTTP status code: 403. A required agreement
// is missing or has expired." — because notarization authenticates as the
// individual, and that individual no longer held the agreements. An API key
// survives an account-holder change.
//
// APPLE_API_KEY accepts BOTH a path and the key's contents, and this is
// deliberate: `notarytool` requires a path on disk, but a GitHub Actions secret
// can only carry the contents. Pasting the whole .p8 (BEGIN PRIVATE KEY … END
// PRIVATE KEY) into the secret is the supported CI shape; we detect the PEM
// header, write it to a 0600 temp file for the duration of the call, and delete
// it afterwards. A value with no PEM header is treated as a path.

const fs = require('fs');
const os = require('os');
const path = require('path');
const { notarize } = require('@electron/notarize');

const APP_BUNDLE_ID = 'com.reliantlabs.reliant';

const DEFAULT_MAX_ATTEMPTS = 3;
const DEFAULT_RETRY_DELAY_MS = 15000;

// Transient failures. Apple's notary service and the stapler both fail this way
// often enough that retrying is worth the wall clock.
const RETRYABLE_ERROR_PATTERNS = [
  /Failed to staple your application with code:\s*68/i,
  /NSURLErrorDomain Code=-1005/i,
  /The network connection was lost/i,
  /api\.apple-cloudkit\.com/i,
  /CloudKit's response is inconsistent/i,
  /timed out/i,
  /ECONNRESET/i,
  /ENOTFOUND/i,
  /EAI_AGAIN/i,
  /Unexpected token/i,
  /not valid JSON/i
];

// Identity failures. Retrying these cannot help — the same credentials will be
// rejected the same way — and doing so triples the time to the real error.
//
// The 403/agreement patterns are the ones that were MISSING when v1.7.14 failed:
// the old code matched only 'Authentication failed', 'Invalid credentials' and
// '401', so a 403 fell through to the retry path and burned all three attempts
// before surfacing a message that blamed the network.
const NON_RETRYABLE_ERROR_PATTERNS = [
  /HTTP status code:\s*403/i,
  /\b403\b.*forbidden/i,
  /A required agreement is missing or has expired/i,
  /agreement.*(missing|expired|not been accepted)/i,
  /Authentication failed/i,
  /Invalid credentials/i,
  /Unable to authenticate/i,
  /HTTP status code:\s*401/i,
  /\b401\b/
];

function parsePositiveInt(rawValue, fallback) {
  const parsed = Number.parseInt(rawValue, 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// notarytool reports the interesting part on stdout/stderr rather than in
// error.message, so classification has to look at all of it.
function buildErrorText(error) {
  return [error?.message, error?.stack, error?.stdout, error?.stderr]
    .filter(Boolean)
    .join('\n');
}

function matchesAny(patterns, text) {
  return patterns.some((pattern) => pattern.test(text));
}

function isIdentityError(error) {
  return matchesAny(NON_RETRYABLE_ERROR_PATTERNS, buildErrorText(error));
}

// Identity errors win over transient ones: a 403 response body can also fail to
// parse as JSON, and "retry the network" is the wrong read of that.
function isRetryableNotarizationError(error) {
  if (isIdentityError(error)) {
    return false;
  }
  return matchesAny(RETRYABLE_ERROR_PATTERNS, buildErrorText(error));
}

function looksLikePrivateKeyContents(value) {
  return /-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(value);
}

/**
 * Resolve Apple credentials from the environment.
 *
 * @returns {{
 *   strategy: 'api-key' | 'apple-id' | 'none',
 *   credentials: object | null,
 *   summary: string[],
 *   missing: string[],
 *   cleanup: () => void,
 * }}
 */
function resolveAppleCredentials() {
  const apiKey = process.env.APPLE_API_KEY;
  const apiKeyId = process.env.APPLE_API_KEY_ID;
  const apiIssuer = process.env.APPLE_API_ISSUER;

  const appleId = process.env.APPLE_ID;
  const appleIdPassword = process.env.APPLE_APP_SPECIFIC_PASSWORD;
  const teamId = process.env.APPLE_TEAM_ID;

  const anyApiKeyVarSet = Boolean(apiKey || apiKeyId || apiIssuer);
  const allApiKeyVarsSet = Boolean(apiKey && apiKeyId && apiIssuer);

  if (allApiKeyVarsSet) {
    let keyPath = apiKey;
    let tempDir = null;

    if (looksLikePrivateKeyContents(apiKey)) {
      // notarytool wants a path; a GitHub secret can only carry contents.
      // 0700 dir + 0600 file, removed in cleanup().
      tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'reliant-notarize-'));
      keyPath = path.join(tempDir, `AuthKey_${apiKeyId}.p8`);
      fs.writeFileSync(keyPath, apiKey.endsWith('\n') ? apiKey : `${apiKey}\n`, {
        mode: 0o600
      });
    } else if (!fs.existsSync(keyPath)) {
      throw new Error(
        `APPLE_API_KEY was treated as a filesystem path but no file exists at "${keyPath}". ` +
          'Set it either to a readable path, or to the literal contents of the .p8 ' +
          '(starting with "-----BEGIN PRIVATE KEY-----"), which will be written to a temp file.'
      );
    }

    return {
      strategy: 'api-key',
      credentials: {
        appleApiKey: keyPath,
        appleApiKeyId: apiKeyId,
        appleApiIssuer: apiIssuer
      },
      summary: [
        'Auth: App Store Connect API key (team-owned)',
        `   Key ID: ${apiKeyId}`,
        `   Issuer: ${apiIssuer}`,
        `   Key source: ${tempDir ? 'APPLE_API_KEY contents → temp file' : `path ${keyPath}`}`
      ],
      missing: [],
      cleanup: () => {
        if (tempDir) {
          fs.rmSync(tempDir, { recursive: true, force: true });
        }
      }
    };
  }

  if (appleId && appleIdPassword && teamId) {
    if (anyApiKeyVarSet) {
      console.warn(
        '⚠️  Partial App Store Connect API key config detected — falling back to Apple ID auth.'
      );
      console.warn(`   APPLE_API_KEY: ${apiKey ? '✓ Set' : '✗ Missing'}`);
      console.warn(`   APPLE_API_KEY_ID: ${apiKeyId ? '✓ Set' : '✗ Missing'}`);
      console.warn(`   APPLE_API_ISSUER: ${apiIssuer ? '✓ Set' : '✗ Missing'}`);
    }

    return {
      strategy: 'apple-id',
      credentials: { appleId, appleIdPassword, teamId },
      summary: [
        'Auth: Apple ID + app-specific password (person-owned — prefer an API key)',
        `   Apple ID: ${appleId}`,
        `   Team ID: ${teamId}`
      ],
      missing: [],
      cleanup: () => {}
    };
  }

  const missing = [];
  if (!apiKey) missing.push('APPLE_API_KEY');
  if (!apiKeyId) missing.push('APPLE_API_KEY_ID');
  if (!apiIssuer) missing.push('APPLE_API_ISSUER');
  if (!appleId) missing.push('APPLE_ID');
  if (!appleIdPassword) missing.push('APPLE_APP_SPECIFIC_PASSWORD');
  if (!teamId) missing.push('APPLE_TEAM_ID');

  return { strategy: 'none', credentials: null, summary: [], missing, cleanup: () => {} };
}

const MISSING_CREDENTIALS_MESSAGE =
  'Notarization credentials missing. Provide EITHER an App Store Connect API key ' +
  '(APPLE_API_KEY + APPLE_API_KEY_ID + APPLE_API_ISSUER — preferred, team-owned) ' +
  'OR an Apple ID (APPLE_ID + APPLE_APP_SPECIFIC_PASSWORD + APPLE_TEAM_ID).';

// This is what a future engineer reads at 2am. Say what the status code means
// about IDENTITY, and name both auth paths, because which one is in use
// determines where to go look.
function printIdentityGuidance(strategy) {
  console.error('🚫 This is an IDENTITY failure, not a transient one — not retrying.');
  console.error('');
  console.error(
    '   A 403 from Apple almost never means "your password is wrong". It means the'
  );
  console.error(
    '   identity that authenticated is not the identity holding the current Apple'
  );
  console.error(
    '   Developer Program agreements. Apple reports this as "A required agreement is'
  );
  console.error(
    '   missing or has expired", which reads like a renewal problem and is not one.'
  );
  console.error('');
  console.error(
    '   This is exactly how v1.7.14 shipped without macOS: the ACCOUNT HOLDER changed.'
  );
  console.error(
    '   Code signing kept working (it uses the certificate); notarization broke'
  );
  console.error('   (it uses the account identity).');
  console.error('');

  if (strategy === 'api-key') {
    console.error('   You are authenticating with an App Store Connect API KEY.');
    console.error(`   Check: APPLE_API_KEY_ID=${process.env.APPLE_API_KEY_ID || '(unset)'}`);
    console.error(`          APPLE_API_ISSUER=${process.env.APPLE_API_ISSUER || '(unset)'}`);
    console.error('   → Is the key still active, and does it have the Developer role?');
    console.error('     App Store Connect → Users and Access → Integrations → Keys');
    console.error('   → Has someone at the team accepted the current agreements?');
    console.error('     https://developer.apple.com/account (Agreements, Tax, and Banking)');
  } else {
    console.error('   You are authenticating with an APPLE ID + app-specific password.');
    console.error(`   Check: APPLE_ID=${process.env.APPLE_ID || '(unset)'}`);
    console.error(`          APPLE_TEAM_ID=${process.env.APPLE_TEAM_ID || '(unset)'}`);
    console.error(
      '   → Is that person still on the team, and did the account holder change?'
    );
    console.error(
      '   → THE DURABLE FIX IS TO STOP USING A PERSON: switch to an App Store Connect'
    );
    console.error(
      '     API key (APPLE_API_KEY / APPLE_API_KEY_ID / APPLE_API_ISSUER). A key belongs'
    );
    console.error('     to the TEAM and survives an account-holder change.');
    console.error('     App Store Connect → Users and Access → Integrations → Keys');
  }
  console.error('');
}

/**
 * Run notarization for an electron-builder afterSign context.
 *
 * @param {object} context electron-builder afterSign context
 * @param {{requireCredentials?: boolean}} options
 *   requireCredentials true  → missing credentials is a hard failure (release builds)
 *   requireCredentials false → missing credentials warns and skips (local builds)
 */
async function runNotarization(context, options = {}) {
  const { requireCredentials = true } = options;
  const { electronPlatformName, appOutDir } = context;

  if (electronPlatformName !== 'darwin') {
    return;
  }

  if (process.env.SKIP_NOTARIZATION === 'true') {
    console.log('⚠️  Notarization skipped (SKIP_NOTARIZATION=true)');
    console.log('    WARNING: Never use SKIP_NOTARIZATION in CI/production');
    return;
  }

  const appName = context.packager.appInfo.productFilename;
  const resolved = resolveAppleCredentials();

  if (resolved.strategy === 'none') {
    if (requireCredentials) {
      throw new Error(MISSING_CREDENTIALS_MESSAGE);
    }
    console.warn(`⚠️ Skipping notarization: ${MISSING_CREDENTIALS_MESSAGE}`);
    console.warn(`   Unset: ${resolved.missing.join(', ')}`);
    return;
  }

  const maxAttempts = parsePositiveInt(
    process.env.NOTARIZE_MAX_ATTEMPTS,
    DEFAULT_MAX_ATTEMPTS
  );
  const baseRetryDelayMs = parsePositiveInt(
    process.env.NOTARIZE_RETRY_DELAY_MS,
    DEFAULT_RETRY_DELAY_MS
  );

  console.log('🍎 Starting notarization...');
  console.log(`   App: ${appName}.app`);
  console.log(`   Bundle ID: ${APP_BUNDLE_ID}`);
  resolved.summary.forEach((line) => console.log(`   ${line}`));
  console.log(
    `   Retry policy: up to ${maxAttempts} attempt(s), base delay ${Math.round(baseRetryDelayMs / 1000)}s`
  );

  try {
    let lastError = null;

    for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
      try {
        if (attempt > 1) {
          console.log(`🔁 Retrying notarization (attempt ${attempt}/${maxAttempts})...`);
        }

        await notarize({
          tool: 'notarytool',
          appBundleId: APP_BUNDLE_ID,
          appPath: `${appOutDir}/${appName}.app`,
          ...resolved.credentials
        });

        console.log('✅ Notarization successful!');
        return;
      } catch (error) {
        lastError = error;
        const errorMessage = error?.message || String(error);
        const identityFailure = isIdentityError(error);
        const retryable = isRetryableNotarizationError(error);
        const hasMoreAttempts = attempt < maxAttempts;

        console.error('');
        if (retryable && hasMoreAttempts) {
          console.error('⚠️  NOTARIZATION ATTEMPT FAILED (transient) - RETRYING');
        } else {
          console.error('❌ NOTARIZATION FAILED - BUILD STOPPED');
        }
        console.error('━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━');
        console.error('');
        console.error('Error:', errorMessage);
        console.error('');

        if (retryable && hasMoreAttempts) {
          const delayMs = baseRetryDelayMs * attempt;
          console.error(
            `Detected transient Apple notarization/stapling failure. Retrying in ${Math.ceil(delayMs / 1000)}s...`
          );
          await sleep(delayMs);
          continue;
        }

        if (identityFailure) {
          printIdentityGuidance(resolved.strategy);
        } else if (
          errorMessage.includes('not valid JSON') ||
          errorMessage.includes('Unexpected token')
        ) {
          console.error('💡 A malformed response from Apple usually means:');
          console.error('   1. A temporary Apple server issue — check');
          console.error('      https://developer.apple.com/system-status/');
          console.error('   2. An HTML error page in place of JSON, which is itself');
          console.error('      usually an auth or agreement problem (see the 403 notes above)');
        }

        if (retryable && !hasMoreAttempts) {
          console.error(`🛑 Exhausted ${maxAttempts} notarization attempt(s).`);
        }

        console.error('');
        console.error('📋 Troubleshooting:');
        console.error(
          '   • Pending agreements: https://developer.apple.com/account (Agreements, Tax, and Banking)'
        );
        if (resolved.strategy === 'api-key') {
          console.error(
            '   • Test the key: xcrun notarytool history --key <path.p8> --key-id <id> --issuer <uuid>'
          );
        } else {
          console.error(
            '   • Test the credentials: xcrun notarytool history --apple-id <email> --team-id <id> --password <pwd>'
          );
        }
        console.error('');

        // Always throw — never continue without notarization.
        throw error;
      }
    }

    // Defensive: the loop above either returns or throws.
    throw lastError || new Error('Notarization failed for an unknown reason');
  } finally {
    resolved.cleanup();
  }
}

module.exports = {
  APP_BUNDLE_ID,
  MISSING_CREDENTIALS_MESSAGE,
  NON_RETRYABLE_ERROR_PATTERNS,
  RETRYABLE_ERROR_PATTERNS,
  isIdentityError,
  isRetryableNotarizationError,
  resolveAppleCredentials,
  runNotarization
};
