#!/usr/bin/env node
'use strict';

const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const packageRoot = path.resolve(__dirname, '..');
const packageJson = require(path.join(packageRoot, 'package.json'));
const outputRoot = path.join(packageRoot, 'npm', 'bin');
const buildChannel = process.env.TURNAL_RELEASE_CHANNEL || inferChannel(packageJson.version);
const buildCommit = process.env.TURNAL_COMMIT || process.env.GITHUB_SHA || '';

const targets = [
  { nodePlatform: 'darwin', nodeArch: 'x64', goos: 'darwin', goarch: 'amd64' },
  { nodePlatform: 'darwin', nodeArch: 'arm64', goos: 'darwin', goarch: 'arm64' },
  { nodePlatform: 'linux', nodeArch: 'x64', goos: 'linux', goarch: 'amd64' },
  { nodePlatform: 'linux', nodeArch: 'arm64', goos: 'linux', goarch: 'arm64' },
  { nodePlatform: 'win32', nodeArch: 'x64', goos: 'windows', goarch: 'amd64' },
  { nodePlatform: 'win32', nodeArch: 'arm64', goos: 'windows', goarch: 'arm64' },
];

function exeName(target) {
  return target.nodePlatform === 'win32' ? 'turnal.exe' : 'turnal';
}

function targetKey(target) {
  return `${target.nodePlatform}-${target.nodeArch}`;
}

function inferChannel(version) {
  return String(version).includes('-nightly.') ? 'nightly' : 'stable';
}

function ldflags() {
  return [
    '-s',
    '-w',
    `-X github.com/AadiJo/turnal/internal/cli.version=${packageJson.version}`,
    `-X github.com/AadiJo/turnal/internal/cli.channel=${buildChannel}`,
    `-X github.com/AadiJo/turnal/internal/cli.commit=${buildCommit}`,
    '-X github.com/AadiJo/turnal/internal/cli.installSource=npm',
  ].join(' ');
}

function buildTarget(target) {
  const key = targetKey(target);
  const outputPath = path.join(outputRoot, key, exeName(target));
  fs.mkdirSync(path.dirname(outputPath), { recursive: true });

  console.log(`building ${key}`);
  const result = spawnSync('go', [
    'build',
    '-trimpath',
    '-buildvcs=false',
    '-ldflags',
    ldflags(),
    '-o',
    outputPath,
    './cmd/turnal',
  ], {
    cwd: packageRoot,
    stdio: 'inherit',
    env: {
      ...process.env,
      CGO_ENABLED: '0',
      GOOS: target.goos,
      GOARCH: target.goarch,
    },
  });

  if (result.error) {
    if (result.error.code === 'ENOENT') {
      throw new Error('go is required to build npm release binaries');
    }
    throw result.error;
  }
  if (result.status !== 0) {
    process.exit(result.status || 1);
  }

  if (target.nodePlatform !== 'win32') {
    fs.chmodSync(outputPath, 0o755);
  }
}

fs.rmSync(outputRoot, { recursive: true, force: true });
for (const target of targets) {
  buildTarget(target);
}
