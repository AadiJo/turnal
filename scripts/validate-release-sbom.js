#!/usr/bin/env node
'use strict';

const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const sourceName = 'Turnal';
const releaseExecutables = [
  'turnal',
  'turnal-adapter-opencode',
  'turnal-adapter-gemini-cli',
  'turnal-adapter-copilot-cli',
];

function usage() {
  console.error('usage: validate-release-sbom.js --sbom <file> --version <version>');
}

function parseArguments(args) {
  if (args.length !== 4 || args[0] !== '--sbom' || args[2] !== '--version') {
    throw new Error('invalid arguments');
  }
  return { sbomPath: args[1], version: args[3] };
}

function addPackage(inventory, name, version) {
  const versions = inventory.get(name) || new Set();
  versions.add(version);
  inventory.set(name, versions);
}

function packageKey(name, version) {
  return `${name}@${version}`;
}

function releasePackagePolicy(repoRoot, releaseVersion) {
  const packageJson = JSON.parse(fs.readFileSync(path.join(repoRoot, 'package.json'), 'utf8'));
  if (packageJson.version !== releaseVersion) {
    throw new Error(
      `release version '${releaseVersion}' does not match package.json version '${packageJson.version}'`,
    );
  }

  const moduleLines = execFileSync(
    'go',
    [
      'list',
      '-m',
      '-f',
      '{{.Path}}\t{{if .Replace}}{{if .Replace.Version}}{{.Replace.Version}}{{else}}UNKNOWN{{end}}{{else}}{{if .Version}}{{.Version}}{{else}}UNKNOWN{{end}}{{end}}',
      'all',
    ],
    { cwd: repoRoot, encoding: 'utf8' },
  )
    .trim()
    .split('\n')
    .filter(Boolean)
    .map((line) => {
      const [name, version] = line.split('\t');
      return { name, version };
    });
  if (moduleLines.length === 0) throw new Error('go list returned no modules');

  const goVersion = execFileSync('go', ['env', 'GOVERSION'], {
    cwd: repoRoot,
    encoding: 'utf8',
  }).trim();
  const allowedPackages = new Map();
  addPackage(allowedPackages, sourceName, releaseVersion);
  addPackage(allowedPackages, packageJson.name, releaseVersion);
  addPackage(allowedPackages, 'stdlib', goVersion);
  for (const { name, version } of moduleLines) addPackage(allowedPackages, name, version);
  for (const executable of releaseExecutables) addPackage(allowedPackages, executable, 'UNKNOWN');

  const requiredPackages = [
    packageKey(sourceName, releaseVersion),
    packageKey(moduleLines[0].name, moduleLines[0].version),
    packageKey('stdlib', goVersion),
    ...releaseExecutables.map((name) => packageKey(name, 'UNKNOWN')),
  ];
  return { allowedPackages, requiredPackages };
}

function validate(sbom, expectedVersion, policy) {
  const failures = [];
  const packages = Array.isArray(sbom.packages) ? sbom.packages : [];

  if (sbom.spdxVersion !== 'SPDX-2.3') {
    failures.push(`unexpected SPDX version '${sbom.spdxVersion || ''}'`);
  }
  if (sbom.name !== sourceName) {
    failures.push(`unexpected SBOM name '${sbom.name || ''}'`);
  }

  const describes = (sbom.relationships || []).find(
    (relationship) =>
      relationship.spdxElementId === 'SPDXRef-DOCUMENT' &&
      relationship.relationshipType === 'DESCRIBES',
  );
  const describedPackage = packages.find(
    (pkg) => pkg.SPDXID === describes?.relatedSpdxElement,
  );
  if (
    describedPackage?.name !== sourceName ||
    describedPackage?.versionInfo !== expectedVersion
  ) {
    failures.push(`SBOM does not describe ${sourceName} ${expectedVersion}`);
  }

  const actualPackages = new Set();
  const unexpectedPackages = new Set();
  for (const pkg of packages) {
    const name = pkg.name || '<unnamed>';
    const version = pkg.versionInfo || 'UNKNOWN';
    const key = packageKey(name, version);
    actualPackages.add(key);
    if (!policy.allowedPackages.get(name)?.has(version)) unexpectedPackages.add(key);
  }
  if (unexpectedPackages.size > 0) {
    failures.push(`unexpected packages: ${[...unexpectedPackages].sort().join(', ')}`);
  }

  const missingPackages = policy.requiredPackages.filter((key) => !actualPackages.has(key));
  if (missingPackages.length > 0) {
    failures.push(`missing release packages: ${missingPackages.sort().join(', ')}`);
  }

  const unrelatedFiles = (sbom.files || [])
    .map((file) => file.fileName || '')
    .filter((name) => /(^|\/)(\.git|apps)(\/|$)/.test(name));
  if (unrelatedFiles.length > 0) {
    failures.push(`unrelated repository files: ${unrelatedFiles.sort().join(', ')}`);
  }

  if (failures.length > 0) {
    throw new Error(`release SBOM validation failed:\n- ${failures.join('\n- ')}`);
  }
}

function main() {
  let options;
  try {
    options = parseArguments(process.argv.slice(2));
  } catch (error) {
    usage();
    throw error;
  }

  const repoRoot = path.resolve(__dirname, '..');
  const sbom = JSON.parse(fs.readFileSync(options.sbomPath, 'utf8'));
  const policy = releasePackagePolicy(repoRoot, options.version);
  validate(sbom, options.version, policy);
  console.log(`validated ${sourceName} ${options.version} SBOM`);
}

if (require.main === module) {
  try {
    main();
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  }
}

module.exports = { addPackage, packageKey, releasePackagePolicy, validate };
