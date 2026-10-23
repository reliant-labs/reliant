// The install record: one id per install, plus whether its first launch has
// been reported as a product event (product.app_first_launch).
//
// The id is minted on first read and kept in userData, so it survives updates
// but not a reinstall that wipes userData. "Reported" is only set once the
// renderer says the event reached the telemetry pipeline, so a launch that
// happens offline, or with analytics off, stays pending and is sent on the
// first launch where telemetry is running. installedAt travels with the event
// so the funnel dates it to the install, not to the flush.
//
// A record that exists but cannot be read is treated as an install that has
// already reported: re-announcing an existing install as new would inflate
// the funnel, and a missed first-launch event cannot be recovered anyway.

const crypto = require("crypto");
const fs = require("fs");
const path = require("path");

const FILE_NAME = "install.json";

function recordPath(userDataDir) {
  return path.join(userDataDir, FILE_NAME);
}

function writeRecord(userDataDir, record, fsImpl) {
  fsImpl.mkdirSync(userDataDir, { recursive: true });
  const target = recordPath(userDataDir);
  const tmp = `${target}.${process.pid}.tmp`;
  fsImpl.writeFileSync(tmp, JSON.stringify(record, null, 2), "utf8");
  fsImpl.renameSync(tmp, target);
}

function isRecord(value) {
  return (
    value &&
    typeof value.installId === "string" &&
    value.installId !== "" &&
    typeof value.firstLaunchReported === "boolean"
  );
}

/**
 * Reads the install record, creating it on the first call for this install.
 * Never throws: the worst case is an in-memory record that is not persisted.
 */
function loadInstallRecord(userDataDir, { fsImpl = fs, now = () => new Date(), uuid = crypto.randomUUID } = {}) {
  let existing = null;
  let missing = false;
  try {
    existing = JSON.parse(fsImpl.readFileSync(recordPath(userDataDir), "utf8"));
  } catch (error) {
    missing = error && error.code === "ENOENT";
  }
  if (isRecord(existing)) {
    return existing;
  }

  const record = {
    installId: uuid(),
    installedAt: now().toISOString(),
    firstLaunchReported: !missing,
  };
  try {
    writeRecord(userDataDir, record, fsImpl);
  } catch {
    // Unwritable userData: the id lasts for this process only.
  }
  return record;
}

/** Records that app_first_launch was delivered. Idempotent. */
function markFirstLaunchReported(userDataDir, options = {}) {
  const fsImpl = options.fsImpl || fs;
  const record = loadInstallRecord(userDataDir, options);
  if (record.firstLaunchReported) {
    return record;
  }
  const updated = { ...record, firstLaunchReported: true };
  try {
    writeRecord(userDataDir, updated, fsImpl);
  } catch {
    // Reported again next launch rather than never.
  }
  return updated;
}

/** What the renderer needs to emit app_first_launch, or to know it need not. */
function installInfo(userDataDir, { platform, arch, version, ...options }) {
  const record = loadInstallRecord(userDataDir, options);
  return {
    installId: record.installId,
    installedAt: record.installedAt,
    firstLaunchPending: !record.firstLaunchReported,
    platform,
    arch,
    version,
  };
}

module.exports = { loadInstallRecord, markFirstLaunchReported, installInfo, FILE_NAME };
