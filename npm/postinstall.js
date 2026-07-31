#!/usr/bin/env node
'use strict';

const fs = require('node:fs');
const path = require('node:path');

const packageJson = require('../package.json');

const packageRoot = path.resolve(__dirname, '..');

function comparablePath(value, platform) {
  const pathApi = platform === 'win32' ? path.win32 : path;
  const normalized = pathApi.resolve(value).replaceAll('\\', '/');
  return platform === 'win32' ? normalized.toLowerCase() : normalized;
}

function shimDirectoriesFor(
  root,
  environment,
  packageName = packageJson.name,
  platform = process.platform,
) {
  const resolvedRoot = path.resolve(root);
  const packageParts = packageName.split('/');

  // A global npm shim lives directly in the configured prefix. Verify that the
  // package is installed under that prefix before considering it global.
  if (environment.npm_config_global === 'true' && environment.npm_config_prefix) {
    const prefix = path.resolve(environment.npm_config_prefix);
    const expectedRoot = path.join(prefix, 'node_modules', ...packageParts);
    if (comparablePath(resolvedRoot, platform) === comparablePath(expectedRoot, platform)) {
      return [prefix];
    }
  }

  // A local npm shim lives in the .bin directory next to the package scope.
  let nodeModulesDirectory = resolvedRoot;
  for (let index = 0; index < packageParts.length; index += 1) {
    nodeModulesDirectory = path.dirname(nodeModulesDirectory);
  }
  const expectedRoot = path.join(nodeModulesDirectory, ...packageParts);
  if (path.basename(nodeModulesDirectory).toLowerCase() === 'node_modules' &&
      comparablePath(resolvedRoot, platform) === comparablePath(expectedRoot, platform)) {
    return [path.join(nodeModulesDirectory, '.bin')];
  }

  return [];
}

function expectedPowerShellShim(relativeTarget) {
  const target = `"$basedir/${relativeTarget.replaceAll('\\', '/')}"`;
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
    $input | & "$basedir/node$exe"  ${target} $args
  } else {
    & "$basedir/node$exe"  ${target} $args
  }
  $ret=$LASTEXITCODE
} else {
  # Support pipeline input
  if ($MyInvocation.ExpectingInput) {
    $input | & "node$exe"  ${target} $args
  } else {
    & "node$exe"  ${target} $args
  }
  $ret=$LASTEXITCODE
}
exit $ret
`;
}

// Match the complete npm cmd-shim output so npm-derived custom scripts are not
// mistaken for launchers owned by this installation.
function isGeneratedShimForTarget(contents, shimDirectory, targetPath, platform) {
  const pathApi = platform === 'win32' ? path.win32 : path;
  const relativeTarget = pathApi.relative(
    pathApi.resolve(shimDirectory),
    pathApi.resolve(targetPath),
  );

  if (pathApi.isAbsolute(relativeTarget)) {
    return false;
  }

  return contents.replaceAll('\r\n', '\n') === expectedPowerShellShim(relativeTarget);
}

function removePowerShellShims({
  root = packageRoot,
  environment = process.env,
  platform = process.platform,
  bins = packageJson.bin,
  filesystem = fs,
} = {}) {
  if (platform !== 'win32') {
    return [];
  }

  const removed = [];
  for (const shimDirectory of shimDirectoriesFor(root, environment, packageJson.name, platform)) {
    for (const [name, relativeTarget] of Object.entries(bins)) {
      const shimPath = path.join(shimDirectory, `${name}.ps1`);
      let contents;
      try {
        contents = filesystem.readFileSync(shimPath, 'utf8');
      } catch (error) {
        if (error.code === 'ENOENT') {
          continue;
        }
        throw new Error(`failed to verify ownership of Turnal PowerShell shim: ${shimPath}`, {
          cause: error,
        });
      }

      const targetPath = path.resolve(root, relativeTarget);
      if (isGeneratedShimForTarget(contents, shimDirectory, targetPath, platform)) {
        try {
          filesystem.unlinkSync(shimPath);
        } catch (error) {
          throw new Error(`failed to remove verified Turnal PowerShell shim: ${shimPath}`, {
            cause: error,
          });
        }
        removed.push(shimPath);
      }
    }
  }

  return removed;
}

if (require.main === module) {
  removePowerShellShims();
}

module.exports = {
  isGeneratedShimForTarget,
  removePowerShellShims,
  shimDirectoriesFor,
};
