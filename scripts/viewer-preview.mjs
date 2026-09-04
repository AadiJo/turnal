import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { cp, mkdtemp, mkdir, rm } from 'node:fs/promises';
import http from 'node:http';
import { isIP } from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// All writes, including embedded assets and the registry, stay in this owned tree.
export async function startPreview({ host = '127.0.0.1' } = {}) {
  if (isIP(host) !== 4) throw new Error('Preview host must be an IPv4 address');
  const directory = await mkdtemp(path.join(os.tmpdir(), 'turnal-preview-'));
  const source = path.join(directory, 'source');
  const fixture = path.join(directory, 'fixture');
  const env = { ...process.env, TURNAL_CONFIG: path.join(directory, 'config.toml'), TURNAL_STATE_DIR: path.join(fixture, 'state'), TURNAL_VISUAL_FIXTURE: fixture };
  let child;
  let proxy;
  let stopping;
  const kill = signal => {
    try {
      if (process.platform === 'win32') child.kill(signal);
      else process.kill(-child.pid, signal);
    } catch (error) {
      if (error.code !== 'ESRCH') throw error;
    }
  };
  const stop = () => stopping ??= (async () => {
    if (proxy) {
      proxy.closeAllConnections();
      await new Promise(resolve => proxy.close(resolve));
    }
    if (child && child.exitCode === null && child.signalCode === null) {
      const exited = once(child, 'exit');
      kill('SIGTERM');
      const timer = setTimeout(() => kill('SIGKILL'), 5000);
      await exited;
      clearTimeout(timer);
    }
    await rm(directory, { recursive: true, force: true });
  })();
  const run = async (command, args, cwd) => {
    child = spawn(command, args, { cwd, env, detached: process.platform !== 'win32', stdio: 'inherit' });
    const [code] = await once(child, 'exit');
    if (code !== 0) throw new Error(`${command} failed (${code})`);
  };
  const onSignal = () => { void stop().then(() => process.exit(130)); };
  process.once('SIGINT', onSignal);
  process.once('SIGTERM', onSignal);
  try {
    await mkdir(source);
    for (const name of ['cmd', 'internal', 'sdk', 'integrations', 'go.mod', 'go.sum']) {
      await cp(path.join(repo, name), path.join(source, name), { recursive: true });
    }
    await run(process.execPath, [path.join(repo, 'node_modules/vite/bin/vite.js'), 'build', '--outDir', path.join(source, 'internal/viewer/web/dist')], repo);
    await run('go', ['test', './internal/viewer', '-run', '^TestCreateVisualFixture$', '-count=1'], source);
    const binary = path.join(directory, process.platform === 'win32' ? 'turnal.exe' : 'turnal');
    await run('go', ['build', '-o', binary, './cmd/turnal'], source);
    child = spawn(binary, ['ui', '--no-open', '--port', '0'], { cwd: directory, env, detached: process.platform !== 'win32', stdio: ['ignore', 'pipe', 'inherit'] });
    const backendURL = await new Promise((resolve, reject) => {
      let output = '';
      const timer = setTimeout(() => reject(new Error('Viewer did not start within 30 seconds')), 30000);
      child.once('error', reject);
      child.once('exit', code => { clearTimeout(timer); reject(new Error(`Viewer exited (${code})`)); });
      child.stdout.on('data', chunk => {
        output += chunk;
        const match = output.match(/Turnal Prism:\s+(http:\/\/[^\s]+)/);
        if (match) { clearTimeout(timer); resolve(new URL(match[1])); }
      });
    });
    // This transport is only for the disposable fixture. The production server
    // keeps its exact loopback Host/Origin checks and launch-secret exchange.
    let expectedHost;
    proxy = http.createServer((request, response) => {
      if (request.headers.host !== expectedHost ||
          (request.headers.origin && request.headers.origin !== `http://${expectedHost}`)) {
        response.writeHead(403).end('Unexpected preview Host or Origin');
        return;
      }
      const headers = { ...request.headers, host: backendURL.host };
      if (headers.origin) headers.origin = backendURL.origin;
      const upstream = http.request({ hostname: backendURL.hostname, port: backendURL.port,
        path: request.url, method: request.method, headers }, result => {
        response.writeHead(result.statusCode, result.headers);
        result.pipe(response);
      });
      upstream.on('error', () => { if (!response.headersSent) response.writeHead(502); response.end(); });
      response.on('close', () => upstream.destroy());
      request.pipe(upstream);
    });
    proxy.listen(0, '127.0.0.1');
    await once(proxy, 'listening');
    expectedHost = `${host}:${proxy.address().port}`;
    child.once('exit', code => {
      if (!stopping) {
        console.error(`Viewer exited unexpectedly (${code})`);
        process.exitCode = 1;
        void stop();
      }
    });
    const url = new URL(backendURL);
    url.host = expectedHost;
    return { url: url.href, directory, stop: async () => {
      process.removeListener('SIGINT', onSignal);
      process.removeListener('SIGTERM', onSignal);
      await stop();
    } };
  } catch (error) {
    process.removeListener('SIGINT', onSignal);
    process.removeListener('SIGTERM', onSignal);
    await stop();
    throw error;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const preview = await startPreview({ host: process.env.TURNAL_PREVIEW_HOST });
  console.log(`Disposable viewer: ${preview.url}\nStop with Ctrl-C. Restart this command after editing source.`);
}
