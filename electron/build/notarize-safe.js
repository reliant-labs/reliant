const { notarize } = require("@electron/notarize");

const DEFAULT_MAX_NOTARIZE_ATTEMPTS = 3;
const DEFAULT_RETRY_DELAY_MS = 15000;

const RETRYABLE_ERROR_PATTERNS = [
  /Failed to staple your application with code:\s*68/i,
  /NSURLErrorDomain Code=-1005/i,
  /The network connection was lost/i,
  /api\.apple-cloudkit\.com/i,
  /CloudKit's response is inconsistent/i,
  /timed out/i,
  /ECONNRESET/i,
  /ENOTFOUND/i,
  /EAI_AGAIN/i
];

function parsePositiveInt(rawValue, fallback) {
  const parsed = Number.parseInt(rawValue, 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function buildErrorText(error) {
  return [
    error?.message,
    error?.stack,
    error?.stdout,
    error?.stderr
  ]
    .filter(Boolean)
    .join("\n");
}

function isRetryableNotarizationError(error) {
  const errorText = buildErrorText(error);
  return RETRYABLE_ERROR_PATTERNS.some((pattern) => pattern.test(errorText));
}

exports.default = async function notarizing(context) {
  const { electronPlatformName, appOutDir } = context;

  if (electronPlatformName !== "darwin") {
    return;
  }

  const appName = context.packager.appInfo.productFilename;
  const appleId = process.env.APPLE_ID;
  const appleIdPassword = process.env.APPLE_APP_SPECIFIC_PASSWORD;
  const teamId = process.env.APPLE_TEAM_ID;

  // Only skip if explicitly set (for local testing only)
  if (process.env.SKIP_NOTARIZATION === "true") {
    console.log("⚠️  Notarization skipped (SKIP_NOTARIZATION=true)");
    console.log("    WARNING: Never use SKIP_NOTARIZATION in CI/production");
    return;
  }

  // Credentials are REQUIRED
  if (!appleId || !appleIdPassword || !teamId) {
    throw new Error(
      "Notarization credentials missing. Required: APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD, APPLE_TEAM_ID"
    );
  }

  const maxAttempts = parsePositiveInt(
    process.env.NOTARIZE_MAX_ATTEMPTS,
    DEFAULT_MAX_NOTARIZE_ATTEMPTS
  );
  const baseRetryDelayMs = parsePositiveInt(
    process.env.NOTARIZE_RETRY_DELAY_MS,
    DEFAULT_RETRY_DELAY_MS
  );

  console.log("🍎 Starting notarization (REQUIRED)...");
  console.log(`   App: ${appName}.app`);
  console.log("   Bundle ID: com.reliantlabs.reliant");
  console.log(`   Team ID: ${teamId}`);
  console.log(`   Apple ID: ${appleId}`);
  console.log(
    `   Retry policy: up to ${maxAttempts} attempt(s), base delay ${Math.round(baseRetryDelayMs / 1000)}s`
  );

  let lastError = null;

  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    try {
      if (attempt > 1) {
        console.log(`🔁 Retrying notarization (attempt ${attempt}/${maxAttempts})...`);
      }

      await notarize({
        tool: "notarytool",
        appBundleId: "com.reliantlabs.reliant",
        appPath: `${appOutDir}/${appName}.app`,
        appleId,
        appleIdPassword,
        teamId
      });

      console.log("✅ Notarization successful!");
      return;
    } catch (error) {
      lastError = error;
      const errorMessage = error?.message || String(error);
      const retryable = isRetryableNotarizationError(error);
      const hasMoreAttempts = attempt < maxAttempts;

      console.error("");
      if (retryable && hasMoreAttempts) {
        console.error("⚠️  NOTARIZATION ATTEMPT FAILED (transient) - RETRYING");
      } else {
        console.error("❌ NOTARIZATION FAILED - BUILD STOPPED");
      }
      console.error("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━");
      console.error("");
      console.error("Error:", errorMessage);
      console.error("");

      if (retryable && hasMoreAttempts) {
        const delayMs = baseRetryDelayMs * attempt;
        console.error(
          `Detected transient Apple notarization/stapling failure. Retrying in ${Math.ceil(delayMs / 1000)}s...`
        );
        await sleep(delayMs);
        continue;
      }

      // Provide specific guidance for common errors
      if (
        errorMessage.includes("not valid JSON") ||
        errorMessage.includes("Unexpected token")
      ) {
        console.error("💡 This error usually means:");
        console.error("   1. Apple requires you to accept a new Developer Agreement");
        console.error("      → Go to https://developer.apple.com/account");
        console.error(
          "   2. Invalid credentials (check APPLE_ID, APPLE_TEAM_ID, APPLE_APP_SPECIFIC_PASSWORD)"
        );
        console.error("   3. Temporary Apple server issue");
      }

      if (retryable && !hasMoreAttempts) {
        console.error(`🛑 Exhausted ${maxAttempts} notarization attempt(s).`);
      }

      console.error("");
      console.error("📋 Troubleshooting:");
      console.error("   • Check for pending agreements: https://developer.apple.com/account");
      console.error(
        "   • Test credentials: xcrun notarytool history --apple-id <email> --team-id <id> --password <pwd>"
      );
      console.error("");

      // Always throw - never continue without notarization
      throw error;
    }
  }

  // Defensive fallback: loop should either return or throw.
  throw lastError || new Error("Notarization failed for an unknown reason");
};
