'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');
const test = require('node:test');

const { addPackage, packageKey, releasePackagePolicy, validate } = require('./validate-release-sbom');

const version = '1.2.3';
const requiredPackagePairs = [
  ['Turnal', version],
  ['github.com/AadiJo/turnal', 'UNKNOWN'],
  ['stdlib', 'go1.26.5'],
  ['turnal', 'UNKNOWN'],
  ['turnal-adapter-opencode', 'UNKNOWN'],
  ['turnal-adapter-gemini-cli', 'UNKNOWN'],
  ['turnal-adapter-copilot-cli', 'UNKNOWN'],
  ['turnal-adapter-cursor', 'UNKNOWN'],
  ['turnal-adapter-pi', 'UNKNOWN'],
];

function policy() {
  const allowedPackages = new Map();
  for (const [name, packageVersion] of requiredPackagePairs) {
    addPackage(allowedPackages, name, packageVersion);
  }
  return {
    allowedPackages,
    requiredPackages: requiredPackagePairs.map(([name, packageVersion]) =>
      packageKey(name, packageVersion),
    ),
  };
}

function validSbom() {
  const packages = requiredPackagePairs.map(([name, packageVersion], index) => ({
    SPDXID: index === 0 ? 'SPDXRef-DocumentRoot-Directory-Turnal' : `SPDXRef-Package-${index}`,
    name,
    versionInfo: packageVersion,
  }));
  return {
    spdxVersion: 'SPDX-2.3',
    name: 'Turnal',
    packages,
    files: [],
    relationships: [
      {
        spdxElementId: 'SPDXRef-DOCUMENT',
        relatedSpdxElement: 'SPDXRef-DocumentRoot-Directory-Turnal',
        relationshipType: 'DESCRIBES',
      },
    ],
  };
}

test('accepts the named Turnal release and expected package inventory', () => {
  assert.doesNotThrow(() => validate(validSbom(), version, policy()));
});

test('rejects packages outside the release inventory', () => {
  const sbom = validSbom();
  sbom.packages.push({
    SPDXID: 'SPDXRef-Package-astro',
    name: 'astro',
    versionInfo: '5.12.0',
  });

  assert.throws(
    () => validate(sbom, version, policy()),
    /unexpected packages: astro@5\.12\.0/,
  );
});

test('rejects repository files outside the packed release artifacts', () => {
  const sbom = validSbom();
  sbom.files.push({ fileName: '/apps/marketing/node_modules/astro/package.json' });

  assert.throws(
    () => validate(sbom, version, policy()),
    /unrelated repository files: \/apps\/marketing/,
  );
});

test('builds the allowlist from the release manifests, not workspace apps', () => {
  const repoRoot = path.resolve(__dirname, '..');
  const currentVersion = require('../package.json').version;
  const currentPolicy = releasePackagePolicy(repoRoot, currentVersion);

  assert.equal(currentPolicy.allowedPackages.has('github.com/AadiJo/turnal'), true);
  assert.equal(currentPolicy.allowedPackages.has('astro'), false);
});
