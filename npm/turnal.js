#!/usr/bin/env node
'use strict';

const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const packageRoot = path.resolve(__dirname, '..');
const packageJson = require(path.join(packageRoot, 'package.json'));
const exeName = process.platform === 'win32' ? 'turnal.exe' : 'turnal';
const platformKey = `${process.platform}-${process.arch}`;
const packagedBinaryPath = path.join(packageRoot, 'npm', 'bin', platformKey, exeName);
const cacheRoot = process.env.TURNAL_NPM_CACHE || defaultCacheRoot();
const cachedBinaryPath = path.join(cacheRoot, packageJson.version, platformKey, exeName);
const buildChannel = process.env.TURNAL_RELEASE_CHANNEL || inferChannel(packageJson.version);
const buildCommit = process.env.TURNAL_COMMIT || process.env.GITHUB_SHA || '';

function defaultCacheRoot() {
  const home = os.homedir();
  if (home) {
    return path.join(home, '.cache', 'turnal', 'npm');
  }
  return path.join(os.tmpdir(), 'turnal-npm-cache');
}

function inferChannel(version) {
  return String(version).includes('-nightly.') ? 'nightly' : 'stable';
}

function ldflags() {
  return [
    `-X github.com/AadiJo/turnal/internal/buildinfo.Version=${packageJson.version}`,
    `-X github.com/AadiJo/turnal/internal/buildinfo.Channel=${buildChannel}`,
    `-X github.com/AadiJo/turnal/internal/buildinfo.Commit=${buildCommit}`,
    '-X github.com/AadiJo/turnal/internal/buildinfo.InstallSource=npm',
  ].join(' ');
}

function resolveBinary() {
  if (!process.env.TURNAL_NPM_FORCE_BUILD && fs.existsSync(packagedBinaryPath)) {
    return packagedBinaryPath;
  }

  return ensureBuiltBinary();
}

function ensureBuiltBinary() {
  if (fs.existsSync(cachedBinaryPath)) {
    return cachedBinaryPath;
  }

  fs.mkdirSync(path.dirname(cachedBinaryPath), { recursive: true });
  const result = spawnSync('go', [
    'build',
    '-buildvcs=false',
    '-ldflags',
    ldflags(),
    '-o',
    cachedBinaryPath,
    './cmd/turnal'
  ], {
    cwd: packageRoot,
    stdio: 'inherit',
    env: process.env
  });

  if (result.error) {
    if (result.error.code === 'ENOENT') {
      console.error(`turnal does not include a prebuilt binary for ${platformKey}, and Go is not installed for a local fallback build.`);
      console.error('Install Go, then run turnal again, or install a turnal release that supports this platform.');
      process.exit(1);
    }
    console.error(`failed to build turnal: ${result.error.message}`);
    process.exit(1);
  }

  if (result.status !== 0) {
    process.exit(result.status || 1);
  }

  return cachedBinaryPath;
}

const binaryPath = resolveBinary();

const child = spawn(binaryPath, process.argv.slice(2), {
  stdio: 'inherit',
  env: process.env
});

child.on('error', (error) => {
  console.error(`failed to run turnal: ${error.message}`);
  process.exit(1);
});

child.on('exit', (code, signal) => {
  if (signal) {
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code || 0);
});
