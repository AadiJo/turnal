'use strict';

const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

module.exports = function runAdapter(command) {
  const packageRoot = path.resolve(__dirname, '..');
  const packageJson = require(path.join(packageRoot, 'package.json'));
  const filename = process.platform === 'win32' ? `${command}.exe` : command;
  const platformKey = `${process.platform}-${process.arch}`;
  const packagedPath = path.join(packageRoot, 'npm', 'bin', platformKey, filename);
  const cacheRoot = process.env.TURNAL_NPM_CACHE || path.join(os.homedir() || os.tmpdir(), '.cache', 'turnal', 'npm');
  const cachedPath = path.join(cacheRoot, packageJson.version, platformKey, filename);

  let binaryPath = packagedPath;
  if (process.env.TURNAL_NPM_FORCE_BUILD || !fs.existsSync(binaryPath)) {
    binaryPath = cachedPath;
    if (!fs.existsSync(binaryPath)) {
      fs.mkdirSync(path.dirname(binaryPath), { recursive: true });
      const result = spawnSync('go', ['build', '-buildvcs=false', '-o', binaryPath, `./cmd/${command}`], {
        cwd: packageRoot,
        stdio: 'inherit',
        env: process.env,
      });
      if (result.error) {
        console.error(`failed to build ${command}: ${result.error.message}`);
        process.exit(1);
      }
      if (result.status !== 0) process.exit(result.status || 1);
    }
  }

  const child = spawn(binaryPath, process.argv.slice(2), { stdio: 'inherit', env: process.env });
  child.on('error', (error) => {
    console.error(`failed to run ${command}: ${error.message}`);
    process.exit(1);
  });
  child.on('exit', (code, signal) => {
    if (signal) process.kill(process.pid, signal);
    else process.exit(code || 0);
  });
};

