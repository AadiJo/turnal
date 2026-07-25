#!/usr/bin/env node
'use strict';

const { spawnSync } = require('node:child_process');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const packageRoot = path.resolve(__dirname, '..');
const packageJson = require(path.join(packageRoot, 'package.json'));
const outputRoot = path.resolve(process.env.TURNAL_RELEASE_OUTPUT || path.join(packageRoot, 'dist', 'releases'));
const buildChannel = process.env.TURNAL_RELEASE_CHANNEL || inferChannel(packageJson.version);
const buildCommit = process.env.TURNAL_COMMIT || process.env.GITHUB_SHA || '';

const allTargets = [
  { name: 'darwin_amd64', goos: 'darwin', goarch: 'amd64' },
  { name: 'darwin_arm64', goos: 'darwin', goarch: 'arm64' },
  { name: 'linux_amd64', goos: 'linux', goarch: 'amd64' },
  { name: 'linux_arm64', goos: 'linux', goarch: 'arm64' },
];

const commands = [
  'turnal',
  'turnal-adapter-opencode',
  'turnal-adapter-gemini-cli',
  'turnal-adapter-copilot-cli',
];

function inferChannel(version) {
  return String(version).includes('-nightly.') ? 'nightly' : 'stable';
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: packageRoot,
    stdio: 'inherit',
    ...options,
  });
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    process.exit(result.status || 1);
  }
}

function ldflags() {
  return [
    '-s',
    '-w',
    `-X github.com/AadiJo/turnal/internal/cli.version=${packageJson.version}`,
    `-X github.com/AadiJo/turnal/internal/cli.channel=${buildChannel}`,
    `-X github.com/AadiJo/turnal/internal/cli.commit=${buildCommit}`,
    '-X github.com/AadiJo/turnal/internal/cli.installSource=standalone',
  ].join(' ');
}

function selectedTargets() {
  const filter = String(process.env.TURNAL_RELEASE_TARGETS || '').trim();
  if (!filter) {
    return allTargets;
  }
  const names = new Set(filter.split(',').map((value) => value.trim()).filter(Boolean));
  const targets = allTargets.filter((target) => names.has(target.name));
  if (targets.length !== names.size) {
    const unknown = [...names].filter((name) => !allTargets.some((target) => target.name === name));
    throw new Error(`unsupported release target(s): ${unknown.join(', ')}`);
  }
  return targets;
}

function buildArchive(target, stagingRoot) {
  const stagingDir = path.join(stagingRoot, target.name);
  fs.mkdirSync(stagingDir, { recursive: true });

  for (const command of commands) {
    const outputPath = path.join(stagingDir, command);
    const args = ['build', '-trimpath', '-buildvcs=false'];
    args.push('-ldflags', command === 'turnal' ? ldflags() : '-s -w');
    args.push('-o', outputPath, `./cmd/${command}`);
    console.log(`building ${command} for ${target.name}`);
    run('go', args, {
      env: {
        ...process.env,
        CGO_ENABLED: '0',
        GOOS: target.goos,
        GOARCH: target.goarch,
      },
    });
    fs.chmodSync(outputPath, 0o755);
  }

  const archiveName = `turnal_${packageJson.version}_${target.name}.tar.gz`;
  const archivePath = path.join(outputRoot, archiveName);
  run('tar', ['-czf', archivePath, '-C', stagingDir, ...commands]);
  return { archiveName, archivePath };
}

function checksum(filePath) {
  return crypto.createHash('sha256').update(fs.readFileSync(filePath)).digest('hex');
}

fs.rmSync(outputRoot, { recursive: true, force: true });
fs.mkdirSync(outputRoot, { recursive: true });
const stagingRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'turnal-release-'));

try {
  const archives = selectedTargets().map((target) => buildArchive(target, stagingRoot));
  const checksums = archives
    .map(({ archiveName, archivePath }) => `${checksum(archivePath)}  ${archiveName}`)
    .join('\n');
  fs.writeFileSync(path.join(outputRoot, 'checksums.txt'), `${checksums}\n`, 'utf8');
  console.log(`wrote ${archives.length} archive(s) and checksums to ${outputRoot}`);
} finally {
  fs.rmSync(stagingRoot, { recursive: true, force: true });
}
