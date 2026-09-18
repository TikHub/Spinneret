// Regenerates protobuf-es code into src/gen using buf and web/buf.gen.yaml.
// Runs from web/. Adds $HOME/go/bin (where `go install` puts buf) and the local
// node_modules/.bin (protoc-gen-es) to PATH, and serializes concurrent buf runs
// with the same lock directory as scripts/buf-generate.sh at the repository root.
import { spawnSync } from 'node:child_process';
import { mkdirSync, rmdirSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { delimiter, dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const webDir = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const lockDir = join(process.env.TMPDIR ?? tmpdir(), 'spinneret-buf.lock');
const lockWaitMs = 120_000;
const lockPollMs = 200;

const env = {
  ...process.env,
  PATH: [join(homedir(), 'go', 'bin'), join(webDir, 'node_modules', '.bin'), process.env.PATH ?? ''].join(
    delimiter,
  ),
};

function sleep(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

function acquireLock() {
  const deadline = Date.now() + lockWaitMs;
  for (;;) {
    try {
      mkdirSync(lockDir);
      return;
    } catch (err) {
      if (err.code !== 'EEXIST') throw err;
      if (Date.now() > deadline) {
        throw new Error(`gen: timed out waiting for lock ${lockDir}`, { cause: err });
      }
      sleep(lockPollMs);
    }
  }
}

acquireLock();
let status;
try {
  const result = spawnSync('buf', ['generate', '--template', 'buf.gen.yaml'], {
    cwd: webDir,
    env,
    stdio: 'inherit',
  });
  if (result.error) {
    console.error(
      `gen: failed to run buf (is it installed in $HOME/go/bin or on PATH?): ${result.error.message}`,
    );
  }
  status = result.status ?? 1;
} finally {
  rmdirSync(lockDir);
}
process.exit(status);
