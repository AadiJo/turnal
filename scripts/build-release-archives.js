#!/usr/bin/env node
'use strict';

const { spawnSync } = require('node:child_process');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const zlib = require('node:zlib');

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
  { name: 'windows_amd64', goos: 'windows', goarch: 'amd64' },
  { name: 'windows_arm64', goos: 'windows', goarch: 'arm64' },
];

const commands = [
  'turnal',
  'turnal-adapter-opencode',
  'turnal-adapter-gemini-cli',
  'turnal-adapter-copilot-cli',
  'turnal-adapter-cursor',
  'turnal-adapter-pi',
];

const documentationFiles = ['LICENSE', 'NOTICE'];
function executableName(target, command) {
  return target.goos === 'windows' ? `${command}.exe` : command;
}

function archiveEntries(target) {
  return [
    ...commands.map((command) => ({ name: executableName(target, command), mode: 0o755 })),
    ...documentationFiles.map((name) => ({ name, mode: 0o644 })),
  ];
}

function isWithin(root, candidate) {
  const relative = path.relative(root, candidate);
  return relative !== '' && relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
}

const buildMarkerName = '.turnal-release-output';

function validateOutputRoot() {
  // Inside the repository only dist/ is a build output. Allowing any repository
  // path would let a typo point the recursive delete below at a source tree.
  const allowedRoots = [path.join(packageRoot, 'dist'), os.tmpdir(), '/tmp'].map((value) => path.resolve(value));
  if (!allowedRoots.some((root) => isWithin(root, outputRoot))) {
    throw new Error('TURNAL_RELEASE_OUTPUT must be inside dist/ or a temporary directory');
  }
}

function resetOutputRoot() {
  // Only reuse a directory this script created; never recursively delete a
  // directory that holds anything else.
  if (fs.existsSync(outputRoot)) {
    if (!fs.existsSync(path.join(outputRoot, buildMarkerName))) {
      const entries = fs.readdirSync(outputRoot);
      if (entries.length > 0) {
        throw new Error(
          `refusing to delete ${outputRoot}: it is not empty and was not created by this script`,
        );
      }
    }
    fs.rmSync(outputRoot, { recursive: true, force: true });
  }
  fs.mkdirSync(outputRoot, { recursive: true });
  fs.writeFileSync(path.join(outputRoot, buildMarkerName), '');
}

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
    `-X github.com/AadiJo/turnal/internal/buildinfo.Version=${packageJson.version}`,
    `-X github.com/AadiJo/turnal/internal/buildinfo.Channel=${buildChannel}`,
    `-X github.com/AadiJo/turnal/internal/buildinfo.Commit=${buildCommit}`,
    '-X github.com/AadiJo/turnal/internal/buildinfo.InstallSource=standalone',
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
    const outputPath = path.join(stagingDir, executableName(target, command));
    const args = ['build', '-trimpath', '-buildvcs=false'];
    args.push('-ldflags', ldflags());
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

  for (const name of documentationFiles) {
    const outputPath = path.join(stagingDir, name);
    fs.copyFileSync(path.join(packageRoot, name), outputPath);
    fs.chmodSync(outputPath, 0o644);
  }

  const archiveName = `turnal_${packageJson.version}_${target.name}.tar.gz`;
  const archivePath = path.join(outputRoot, archiveName);
  writeArchive(archivePath, stagingDir, target);
  return { archiveName, archivePath };
}

function writeOctal(header, offset, length, value) {
  const text = Math.trunc(value).toString(8).padStart(length - 1, '0');
  if (text.length > length - 1) {
    throw new Error(`tar field value ${value} does not fit in ${length} bytes`);
  }
  header.write(text, offset, length - 1, 'ascii');
  header[offset + length - 1] = 0;
}

function tarHeader(name, size, mode) {
  const header = Buffer.alloc(512);
  header.write(name, 0, 100, 'utf8');
  writeOctal(header, 100, 8, mode);
  writeOctal(header, 108, 8, 0);
  writeOctal(header, 116, 8, 0);
  writeOctal(header, 124, 12, size);
  writeOctal(header, 136, 12, 0);
  header.fill(0x20, 148, 156);
  header[156] = '0'.charCodeAt(0);
  header.write('ustar\0', 257, 6, 'ascii');
  header.write('00', 263, 2, 'ascii');
  header.write('root', 265, 32, 'ascii');
  header.write('root', 297, 32, 'ascii');
  const checksum = header.reduce((sum, byte) => sum + byte, 0);
  const checksumText = checksum.toString(8).padStart(6, '0');
  header.write(checksumText, 148, 6, 'ascii');
  header[154] = 0;
  header[155] = 0x20;
  return header;
}

function writeArchive(archivePath, stagingDir, target) {
  const chunks = [];
  for (const { name, mode } of archiveEntries(target)) {
    const data = fs.readFileSync(path.join(stagingDir, name));
    chunks.push(tarHeader(name, data.length, mode), data);
    const padding = (512 - (data.length % 512)) % 512;
    if (padding > 0) {
      chunks.push(Buffer.alloc(padding));
    }
  }
  chunks.push(Buffer.alloc(1024));
  const tarball = Buffer.concat(chunks);
  fs.writeFileSync(archivePath, zlib.gzipSync(tarball, { level: 9, mtime: 0 }));
}

function checksum(filePath) {
  return crypto.createHash('sha256').update(fs.readFileSync(filePath)).digest('hex');
}

validateOutputRoot();
resetOutputRoot();
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
