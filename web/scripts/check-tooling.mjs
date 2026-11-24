import assert from 'node:assert/strict';
import { mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from 'vite';

const webRoot = fileURLToPath(new URL('../', import.meta.url));
const fixture = await mkdtemp(path.join(webRoot, '.tooling-check-'));

try {
  await writeFile(
    path.join(fixture, 'index.html'),
    '<!doctype html><html><body><div id="app"></div><script type="module" src="/main.js"></script></body></html>',
  );
  await writeFile(
    path.join(fixture, 'main.js'),
    "import { createApp } from 'vue'; import App from './App.vue'; import './style.css'; createApp(App).mount('#app');",
  );
  await writeFile(
    path.join(fixture, 'App.vue'),
    `
<script setup>
import { ShieldCheck } from '@lucide/vue';
</script>
<template><main class="font-bold"><ShieldCheck />Toolchain smoke test</main></template>
`,
  );
  await writeFile(
    path.join(fixture, 'style.css'),
    '@import "tailwindcss" source(none);\n@source "./App.vue";\n',
  );

  await build({
    configFile: path.join(webRoot, 'vite.config.js'),
    root: fixture,
    logLevel: 'warn',
  });

  const assets = path.join(fixture, 'dist', 'assets');
  const files = await readdir(assets);
  assert(
    files.some((file) => file.endsWith('.js')),
    'Vue and Lucide must produce JavaScript',
  );
  const cssFiles = files.filter((file) => file.endsWith('.css'));
  const css = (
    await Promise.all(cssFiles.map((file) => readFile(path.join(assets, file), 'utf8')))
  ).join('\n');
  assert(css.includes('.font-bold'), 'Tailwind must generate the requested utility');
  console.log('Frontend tooling verified: Vue, Lucide, Tailwind, and Vite compile together.');
} finally {
  await rm(fixture, { recursive: true, force: true });
}
