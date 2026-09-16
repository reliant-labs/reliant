// `npm test` names its test files ONE BY ONE, and nothing checked that the
// list matched what is on disk. So a test file could be added, be perfectly
// green locally when run directly, and never once execute in CI.
//
// That is not hypothetical. When this guard was written, three of the
// directory's files were absent from the script: a brand-new one, plus two
// that have been dead since the initial open-source commit — long enough for
// one of their subjects to be deleted out from under it without anything
// going red.
//
// The obvious fix is a glob, and it is the wrong one here: a glob would have
// silently adopted those two broken legacy files and turned the suite red for
// reasons unrelated to whoever next touched it. A glob also records no reason
// for an omission, so "not run" and "deliberately not run yet" stay
// indistinguishable — which is the actual defect.
//
// So the list stays explicit, and this test makes it total: every *.test.js
// file must be either in the script or in QUARANTINE below, with a reason.
// Adding a test file and forgetting the script now fails immediately, and
// parking a file requires saying why in writing.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');

const ELECTRON_DIR = path.join(__dirname, '..');

// Files that exist, do not run, and are known not to run. Each entry must
// carry the reason it is parked and what it would take to unpark it —
// otherwise this becomes the same silence it was written to prevent.
const QUARANTINE = {
  // Its subject, src/oauth-callback-server.js, no longer exists: the loopback
  // OAuth flow was reworked into src/oauth-loopback.js + src/oauth-contract.js,
  // both of which have their own live tests in the script. Nothing here is
  // salvageable against the current code; it is waiting on deletion, not on
  // repair.
  'oauth-callback-server.test.js':
    'subject module deleted (superseded by oauth-loopback + oauth-contract, both tested)',

  // Its subject, src/window-manager.js, DOES still exist, so its ~20
  // assertions have real value — but the file is written against mocha/chai/
  // sinon (`expect(...).to.equal`, `spy.calledOnce`), and this repo
  // standardized on node:test + node:assert, so neither chai nor sinon is a
  // dependency. Unparking it is a mechanical ~221-line port, not a fix.
  'window-manager.test.js':
    'written for mocha/chai/sinon; needs a port to node:test + node:assert',
};

test('every test file is either in the npm test script or explicitly quarantined', () => {
  const pkg = JSON.parse(fs.readFileSync(path.join(ELECTRON_DIR, 'package.json'), 'utf8'));
  const script = pkg.scripts.test;

  const listed = new Set(
    script
      .split(/\s+/)
      .filter((tok) => tok.endsWith('.test.js'))
      .map((tok) => path.basename(tok))
  );

  const onDisk = fs
    .readdirSync(path.join(ELECTRON_DIR, 'test'))
    .filter((f) => f.endsWith('.test.js'));

  const unaccounted = onDisk.filter((f) => !listed.has(f) && !(f in QUARANTINE));

  assert.deepEqual(
    unaccounted,
    [],
    `these test files would never run: ${unaccounted.join(', ')}. ` +
      `Add each to the "test" script in electron/package.json, or to QUARANTINE ` +
      `in this file with the reason it cannot run yet.`
  );
});

test('quarantined files still exist, and none has quietly been fixed', () => {
  const pkg = JSON.parse(fs.readFileSync(path.join(ELECTRON_DIR, 'package.json'), 'utf8'));
  const listed = new Set(
    pkg.scripts.test
      .split(/\s+/)
      .filter((tok) => tok.endsWith('.test.js'))
      .map((tok) => path.basename(tok))
  );

  for (const [file, reason] of Object.entries(QUARANTINE)) {
    assert.ok(reason && reason.length > 10, `quarantine entry for ${file} needs a real reason`);
    // A deleted file must be removed from the list too, or the list rots into
    // a record of things that no longer exist.
    assert.ok(
      fs.existsSync(path.join(ELECTRON_DIR, 'test', file)),
      `${file} is quarantined but no longer exists — drop it from QUARANTINE`
    );
    // And a file that got repaired and added to the script must not stay
    // parked here, claiming to be broken.
    assert.ok(
      !listed.has(file),
      `${file} is in the npm test script AND quarantined — remove it from QUARANTINE`
    );
  }
});
