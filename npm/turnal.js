#!/usr/bin/env node
'use strict';

const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const packageRoot = path.resolve(__dirname, '..');
const packageJson = require(path.join(packageRoot, 'package.json'));
const exeName = process.platform === 'win32' ? 'turnal.exe' : 'turnal';
const cacheRoot = process.env.TURNAL_NPM_CACHE || defaultCacheRoot();
const binaryPath = path.join(cacheRoot, packageJson.version, `${process.platform}-${process.arch}`, exeName);

function defaultCacheRoot() {
  const home = os.homedir();
  if (home) {
    return path.join(home, '.cache', 'turnal', 'npm');
  }
  return path.join(os.tmpdir(), 'turnal-npm-cache');
}

function ensureBinary() {
  if (fs.existsSync(binaryPath)) {
    return;
  }

  fs.mkdirSync(path.dirname(binaryPath), { recursive: true });
  const result = spawnSync('go', ['build', '-o', binaryPath, './cmd/turnal'], {
    cwd: packageRoot,
    stdio: 'inherit',
    env: process.env
  });

  if (result.error) {
    if (result.error.code === 'ENOENT') {
      console.error('turnal installed from npm builds the Go CLI locally. Install Go, then run turnal again.');
      process.exit(1);
    }
    console.error(`failed to build turnal: ${result.error.message}`);
    process.exit(1);
  }

  if (result.status !== 0) {
    process.exit(result.status || 1);
  }
}

ensureBinary();

const child = spawn(binaryPath, process.argv.slice(2), {
  stdio: 'inherit',
  env: process.env
});

child.on('error', (error) => {
  console.error(`failed to run turnal: ${error.message}`);
  process.exit(1);
});

child.on('exit', (code, signal) => {
  if (signal) {
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code || 0);
});
