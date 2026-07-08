#!/usr/bin/env node
'use strict';

const fs = require('node:fs');
const path = require('node:path');

function usage() {
  console.error('usage: resolve-release.js stable <tag>');
  console.error('       resolve-release.js nightly <date:YYYYMMDD> <run-number> <sha>');
}

function readPackageVersion() {
  const packageJsonPath = path.resolve(__dirname, '..', 'package.json');
  const packageJson = JSON.parse(fs.readFileSync(packageJsonPath, 'utf8'));
  if (typeof packageJson.version !== 'string' || packageJson.version.length === 0) {
    throw new Error(`invalid package version in ${packageJsonPath}`);
  }
  return packageJson.version;
}

function nextPatchVersion(version) {
  const stableCore = version.replace(/[-+].*$/, '');
  const match = /^(\d+)\.(\d+)\.(\d+)$/.exec(stableCore);
  if (!match) {
    throw new Error(`invalid package version '${version}'`);
  }
  const [, major, minor, patch] = match;
  return `${major}.${minor}.${Number(patch) + 1}`;
}

function validateVersion(version) {
  if (!/^\d+\.\d+\.\d+([.-][0-9A-Za-z.-]+)?$/.test(version)) {
    throw new Error(`invalid release version '${version}'`);
  }
}

function writeOutput(entries) {
  const lines = Object.entries(entries).map(([key, value]) => `${key}=${value}`);
  for (const line of lines) {
    console.log(line);
  }
  if (process.env.GITHUB_OUTPUT) {
    fs.appendFileSync(process.env.GITHUB_OUTPUT, `${lines.join('\n')}\n`);
  }
}

function stable(tag) {
  if (!tag) {
    throw new Error('stable release requires a tag');
  }
  const version = tag.replace(/^v/, '');
  validateVersion(version);
  writeOutput({
    channel: 'stable',
    version,
    tag: `v${version}`,
    name: `Turnal v${version}`,
    npm_tag: 'latest',
    prerelease: /^[0-9]+\.[0-9]+\.[0-9]+$/.test(version) ? 'false' : 'true',
    make_latest: /^[0-9]+\.[0-9]+\.[0-9]+$/.test(version) ? 'true' : 'false',
  });
}

function nightly(date, runNumber, sha) {
  if (!/^\d{8}$/.test(date || '')) {
    throw new Error(`invalid nightly date '${date}'`);
  }
  if (!/^[1-9]\d*$/.test(runNumber || '')) {
    throw new Error(`invalid run number '${runNumber}'`);
  }
  if (!/^[0-9a-f]{7,40}$/i.test(sha || '')) {
    throw new Error(`invalid sha '${sha}'`);
  }

  const baseVersion = nextPatchVersion(readPackageVersion());
  const version = `${baseVersion}-nightly.${date}.${runNumber}`;
  writeOutput({
    channel: 'nightly',
    base_version: baseVersion,
    version,
    tag: `v${version}`,
    name: `Turnal Nightly ${version} (${sha.slice(0, 12)})`,
    npm_tag: 'nightly',
    prerelease: 'true',
    make_latest: 'false',
  });
}

try {
  const [, , command, ...args] = process.argv;
  if (command === 'stable') {
    stable(args[0]);
  } else if (command === 'nightly') {
    nightly(args[0], args[1], args[2]);
  } else {
    usage();
    process.exit(2);
  }
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exit(1);
}
