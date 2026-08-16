'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const packageJson = require('../package.json');
const {
  isGeneratedShimForTarget,
  removePowerShellShims,
} = require('../npm/postinstall');

function generatedShim(relativeTarget) {
  return `#!/usr/bin/env pwsh
$basedir=Split-Path $MyInvocation.MyCommand.Definition -Parent

$exe=""
if ($PSVersionTable.PSVersion -lt "6.0" -or $IsWindows) {
  # Fix case when both the Windows and Linux builds of Node
  # are installed in the same directory
  $exe=".exe"
}
$ret=0
if (Test-Path "$basedir/node$exe") {
  # Support pipeline input
  if ($MyInvocation.ExpectingInput) {
    $input | & "$basedir/node$exe"  "$basedir/${relativeTarget}" $args
  } else {
    & "$basedir/node$exe"  "$basedir/${relativeTarget}" $args
  }
  $ret=$LASTEXITCODE
} else {
  # Support pipeline input
  if ($MyInvocation.ExpectingInput) {
    $input | & "node$exe"  "$basedir/${relativeTarget}" $args
  } else {
    & "node$exe"  "$basedir/${relativeTarget}" $args
  }
  $ret=$LASTEXITCODE
}
exit $ret
`;
}

function createInstall() {
  const prefix = fs.mkdtempSync(path.join(os.tmpdir(), 'turnal-postinstall-'));
  const root = path.join(prefix, 'node_modules', '@aadijo', 'turnal');
  fs.mkdirSync(root, { recursive: true });
  return { prefix, root };
}

test('does nothing outside Windows', (t) => {
  const { prefix, root } = createInstall();
  t.after(() => fs.rmSync(prefix, { recursive: true, force: true }));

  const shimPath = path.join(prefix, 'turnal.ps1');
  fs.writeFileSync(shimPath, generatedShim('node_modules/@aadijo/turnal/npm/turnal.js'));

  assert.deepEqual(removePowerShellShims({ root, platform: 'linux' }), []);
  assert.equal(fs.existsSync(shimPath), true);
});

test('a local install removes only its node_modules .bin shim', (t) => {
  const { prefix, root } = createInstall();
  t.after(() => fs.rmSync(prefix, { recursive: true, force: true }));

  const localBin = path.join(prefix, 'node_modules', '.bin');
  fs.mkdirSync(localBin, { recursive: true });

  const projectShim = path.join(prefix, 'turnal.ps1');
  const localShim = path.join(localBin, 'turnal.ps1');
  fs.writeFileSync(projectShim, generatedShim('node_modules/@aadijo/turnal/npm/turnal.js'));
  fs.writeFileSync(localShim, generatedShim('../@aadijo/turnal/npm/turnal.js'));

  const removed = removePowerShellShims({
    root,
    environment: { npm_config_global: 'false', npm_config_prefix: prefix },
    platform: 'win32',
  });

  assert.deepEqual(removed, [localShim]);
  assert.equal(fs.existsSync(localShim), false);
  assert.equal(fs.existsSync(projectShim), true);
});

test('a global install removes only the shim in its verified npm prefix', (t) => {
  const { prefix, root } = createInstall();
  t.after(() => fs.rmSync(prefix, { recursive: true, force: true }));

  const localBin = path.join(prefix, 'node_modules', '.bin');
  fs.mkdirSync(localBin, { recursive: true });

  const globalShim = path.join(prefix, 'turnal.ps1');
  const localShim = path.join(localBin, 'turnal.ps1');
  fs.writeFileSync(globalShim, generatedShim('node_modules/@aadijo/turnal/npm/turnal.js'));
  fs.writeFileSync(localShim, generatedShim('../@aadijo/turnal/npm/turnal.js'));

  const removed = removePowerShellShims({
    root,
    environment: { npm_config_global: 'true', npm_config_prefix: prefix },
    platform: 'win32',
  });

  assert.deepEqual(removed, [globalShim]);
  assert.equal(fs.existsSync(globalShim), false);
  assert.equal(fs.existsSync(localShim), true);
});

test('keeps same-name shims that do not belong to this install', (t) => {
  const { prefix, root } = createInstall();
  t.after(() => fs.rmSync(prefix, { recursive: true, force: true }));

  const unrelatedShim = path.join(prefix, 'turnal.ps1');
  const customShim = path.join(prefix, 'turnal-adapter-cursor.ps1');
  const npmDerivedShim = path.join(prefix, 'turnal-adapter-copilot-cli.ps1');
  const commentOnlyShim = path.join(prefix, 'turnal-adapter-opencode.ps1');
  fs.writeFileSync(unrelatedShim, generatedShim('node_modules/other-package/turnal.js'));
  fs.writeFileSync(customShim, '& "$PSScriptRoot/node_modules/@aadijo/turnal/npm/turnal-adapter-cursor.js" $args\n');
  fs.writeFileSync(
    npmDerivedShim,
    generatedShim('node_modules/@aadijo/turnal/npm/turnal-adapter-copilot-cli.js')
      .replace('$ret=0\n', '# Keep this project-specific setup.\n$ret=0\n'),
  );
  fs.writeFileSync(commentOnlyShim, `#!/usr/bin/env pwsh
$basedir=Split-Path $MyInvocation.MyCommand.Definition -Parent
# Previous target: "$basedir/node_modules/@aadijo/turnal/npm/turnal-adapter-opencode.js"
Write-Output "custom launcher"
`);

  assert.deepEqual(removePowerShellShims({
    root,
    environment: { npm_config_global: 'true', npm_config_prefix: prefix },
    platform: 'win32',
  }), []);
  assert.equal(fs.existsSync(unrelatedShim), true);
  assert.equal(fs.existsSync(customShim), true);
  assert.equal(fs.existsSync(npmDerivedShim), true);
  assert.equal(fs.existsSync(commentOnlyShim), true);
});

test('checks every declared Turnal executable', (t) => {
  const { prefix, root } = createInstall();
  t.after(() => fs.rmSync(prefix, { recursive: true, force: true }));

  for (const [name, target] of Object.entries(packageJson.bin)) {
    fs.writeFileSync(
      path.join(prefix, `${name}.ps1`),
      generatedShim(path.posix.join('node_modules/@aadijo/turnal', target)),
    );
  }

  const removed = removePowerShellShims({
    root,
    environment: { npm_config_global: 'true', npm_config_prefix: prefix },
    platform: 'win32',
  });

  assert.equal(removed.length, Object.keys(packageJson.bin).length);
  for (const name of Object.keys(packageJson.bin)) {
    assert.equal(fs.existsSync(path.join(prefix, `${name}.ps1`)), false);
  }
});

test('accepts npm traversal but rejects other traversal and mixed separators', () => {
  const shimDirectory = 'C:\\repo\\node_modules\\.bin';
  const targetPath = 'C:\\repo\\node_modules\\@aadijo\\turnal\\npm\\turnal.js';

  assert.equal(
    isGeneratedShimForTarget(
      generatedShim('../@aadijo/turnal/npm/turnal.js'),
      shimDirectory,
      targetPath,
      'win32',
    ),
    true,
  );
  assert.equal(
    isGeneratedShimForTarget(
      generatedShim('..\\..\\project-owned.js'),
      shimDirectory,
      targetPath,
      'win32',
    ),
    false,
  );
  assert.equal(
    isGeneratedShimForTarget(
      generatedShim('..\\@aadijo/turnal\\npm/turnal.js'),
      shimDirectory,
      targetPath,
      'win32',
    ),
    false,
  );
});

test('rejects absolute shim targets even when they name the Turnal binary', () => {
  const shimDirectory = 'C:\\repo\\node_modules\\.bin';
  const targetPath = 'C:\\repo\\node_modules\\@aadijo\\turnal\\npm\\turnal.js';

  assert.equal(
    isGeneratedShimForTarget(
      generatedShim(targetPath),
      shimDirectory,
      targetPath,
      'win32',
    ),
    false,
  );
});

test('filesystem failures name the shim invariant and preserve the cause', (t) => {
  const { prefix, root } = createInstall();
  t.after(() => fs.rmSync(prefix, { recursive: true, force: true }));

  const shimPath = path.join(prefix, 'node_modules', '.bin', 'turnal.ps1');
  const readCause = Object.assign(new Error('access denied'), { code: 'EACCES' });
  assert.throws(
    () => removePowerShellShims({
      root,
      environment: {},
      platform: 'win32',
      bins: { turnal: 'npm/turnal.js' },
      filesystem: {
        readFileSync() {
          throw readCause;
        },
      },
    }),
    (error) => {
      assert.equal(error.message, `failed to verify ownership of Turnal PowerShell shim: ${shimPath}`);
      assert.equal(error.cause, readCause);
      return true;
    },
  );

  const unlinkCause = Object.assign(new Error('file is locked'), { code: 'EBUSY' });
  assert.throws(
    () => removePowerShellShims({
      root,
      environment: {},
      platform: 'win32',
      bins: { turnal: 'npm/turnal.js' },
      filesystem: {
        readFileSync() {
          return generatedShim('../@aadijo/turnal/npm/turnal.js');
        },
        unlinkSync() {
          throw unlinkCause;
        },
      },
    }),
    (error) => {
      assert.equal(error.message, `failed to remove verified Turnal PowerShell shim: ${shimPath}`);
      assert.equal(error.cause, unlinkCause);
      return true;
    },
  );
});
