import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readdir, readFile } from 'node:fs/promises';
import { argv } from 'node:process';

const assets = new URL('../dist/assets/', import.meta.url);
const development = argv.includes('--development');
const binary = argv.slice(2).find((argument) => argument !== '--development');
const scripts = (await readdir(assets)).filter((entry) => entry.endsWith('.js'));
assert.ok(scripts.length > 0, 'JavaScript assets are missing.');
let hasWebMCP = false;
for (const entry of scripts) {
  const source = await readFile(new URL(entry, assets), 'utf8');
  for (const marker of [
    '/api-keys',
    '/development/keys',
    'Development API key',
    'Create API key',
  ]) {
    assert.ok(
      !source.includes(marker),
      `${entry} contains an internal API user interface: ${marker}`,
    );
  }
  hasWebMCP ||= source.includes('piped_list_users');
  if (!development) {
    assert.ok(!source.includes('piped_open_credential_form'), `${entry} contains WebMCP code.`);
  }
}
assert.equal(hasWebMCP, development, 'WebMCP assets must exist only in development builds.');
console.log(`${development ? 'Development' : 'Production'} assets verified; no API-key GUI.`);

if (binary) {
  const metadata = spawnSync('go', ['version', '-m', binary], { encoding: 'utf8' });
  assert.equal(metadata.status, 0, 'Could not inspect the binary.');
  assert.match(metadata.stdout, /CGO_ENABLED=0\b/, 'Binaries must have CGO disabled.');
  assert.ok(
    metadata.stdout.includes('modernc.org/sqlite'),
    'Binaries must use the pure-Go SQLite driver.',
  );
  assert.ok(
    !metadata.stdout.includes('golang.org/x/vuln'),
    'The vulnerability scanner must remain outside application binaries.',
  );
  assert.equal(
    metadata.stdout.includes('github.com/modelcontextprotocol/'),
    development,
    'MCP SDK must exist only in development binaries.',
  );
  const symbols = spawnSync('go', ['tool', 'nm', binary], {
    encoding: 'utf8',
    maxBuffer: 32 * 1024 * 1024,
  });
  assert.equal(symbols.status, 0, 'Could not inspect binary symbols.');
  assert.equal(
    symbols.stdout.includes('piped/internal/auth.NewAPIKey'),
    development,
    'API key issuance must be linked only in development binaries.',
  );
  assert.equal(
    symbols.stdout.includes('piped/internal/httpserver/development.(*server).developmentAuth'),
    development,
    'Development HTTP authentication must be linked only in development binaries.',
  );
  if (!development) {
    for (const marker of [
      'github.com/modelcontextprotocol/',
      'piped/internal/httpserver/development.(*server).',
      'piped/internal/store.(*Store).CreateLocalAPIKey',
      'piped/internal/store.(*Store).ResolveAPIKey',
      'piped/internal/store.(*Store).DeleteCompletedRun',
    ]) {
      assert.ok(!symbols.stdout.includes(marker), `Production binary contains ${marker}.`);
    }
  }
  console.log(`${development ? 'Development' : 'Production'} binary verified.`);
}
