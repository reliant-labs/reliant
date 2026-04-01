#!/usr/bin/env node

const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const { execSync } = require('child_process');

function shortHash(input) {
  return crypto.createHash('sha1').update(input).digest('hex').slice(0, 8);
}

function sanitizeDbName(raw) {
  let out = raw
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/_+/g, '_')
    .replace(/^_+|_+$/g, '');

  if (!out) {
    out = 'reliant_dev';
  }

  if (out.length > 63) {
    const suffix = shortHash(out);
    const prefixLen = 63 - 1 - suffix.length;
    out = `${out.slice(0, prefixLen)}_${suffix}`;
  }

  return out;
}

function deriveWorktreeDbName(projectRoot) {
  const normalized = projectRoot.replace(/\\/g, '/');
  const m = normalized.match(/\.reliant\/worktrees\/([^/]+)\/([^/]+)/);

  let repoToken;
  let worktreeToken;

  if (m) {
    repoToken = m[1];
    worktreeToken = m[2];
  } else {
    let repoTop = projectRoot;
    try {
      repoTop = execSync('git rev-parse --show-toplevel', {
        cwd: projectRoot,
        encoding: 'utf8',
        stdio: ['ignore', 'pipe', 'ignore'],
      }).trim() || projectRoot;
    } catch {
      // keep projectRoot fallback
    }

    repoToken = path.basename(repoTop);
    worktreeToken = path.basename(projectRoot);
  }

  return sanitizeDbName(`reliant_${repoToken}_${worktreeToken}`);
}

function parseEnvPorts(filePath) {
  if (!fs.existsSync(filePath)) {
    return {};
  }

  const result = {};
  const lines = fs.readFileSync(filePath, 'utf8').split(/\r?\n/);
  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#')) {
      continue;
    }

    // Supports: export KEY=VALUE and KEY=VALUE
    const normalized = trimmed.startsWith('export ') ? trimmed.slice(7) : trimmed;
    const eqIdx = normalized.indexOf('=');
    if (eqIdx <= 0) {
      continue;
    }

    const key = normalized.slice(0, eqIdx).trim();
    const value = normalized.slice(eqIdx + 1).trim();
    if (key) {
      result[key] = value;
    }
  }

  return result;
}

function getEffectiveVar(name, envPorts) {
  if (process.env[name]) {
    return process.env[name];
  }
  return envPorts[name] || '';
}

function main() {
  const projectRoot = process.cwd();
  const envPortsPath = path.join(projectRoot, '.env.ports');
  const envPorts = parseEnvPorts(envPortsPath);

  const computedDbName = deriveWorktreeDbName(projectRoot);
  const driver = getEffectiveVar('DATABASE_DRIVER', envPorts) || 'sqlite';
  const dbUrl = getEffectiveVar('DATABASE_URL', envPorts);
  const pgDatabase = getEffectiveVar('PGDATABASE', envPorts);

  console.log('Reliant DB Context');
  console.log('------------------');
  console.log(`Project root:      ${projectRoot}`);
  console.log(`Computed PG DB:    ${computedDbName}`);
  console.log(`DATABASE_DRIVER:   ${driver}`);
  console.log(`PGDATABASE:        ${pgDatabase || '(unset)'}`);
  console.log(`DATABASE_URL:      ${dbUrl || '(unset)'}`);
  console.log(`.env.ports loaded: ${fs.existsSync(envPortsPath) ? 'yes' : 'no'}`);

  if (driver === 'postgres' && !dbUrl) {
    console.log('\n⚠️  Postgres mode is active but DATABASE_URL is unset.');
    console.log('   Run your dev startup (e.g. npm run dev:pg) to bootstrap and export it.');
  }
}

main();
