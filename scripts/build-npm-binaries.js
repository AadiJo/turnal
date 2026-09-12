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

const commands = [
  'turnal',
  'turnal-adapter-opencode',
  'turnal-adapter-copilot-cli',
  'turnal-adapter-cursor',
  'turnal-adapter-pi',
];

function exeName(target, command) {
  return target.nodePlatform === 'win32' ? `${command}.exe` : command;
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
    `-X github.com/AadiJo/turnal/internal/buildinfo.Version=${packageJson.version}`,
    `-X github.com/AadiJo/turnal/internal/buildinfo.Channel=${buildChannel}`,
    `-X github.com/AadiJo/turnal/internal/buildinfo.Commit=${buildCommit}`,
    '-X github.com/AadiJo/turnal/internal/buildinfo.InstallSource=npm',
  ].join(' ');
}

function buildTarget(target) {
  const key = targetKey(target);
  for (const command of commands) {
    const outputPath = path.join(outputRoot, key, exeName(target, command));
    fs.mkdirSync(path.dirname(outputPath), { recursive: true });

    console.log(`building ${command} for ${key}`);
    const args = [
      'build',
      '-trimpath',
      '-buildvcs=false',
    ];
    args.push('-ldflags', ldflags());
    args.push('-o', outputPath, `./cmd/${command}`);
    const result = spawnSync('go', args, {
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
}

fs.rmSync(outputRoot, { recursive: true, force: true });
for (const target of targets) {
  buildTarget(target);
}
