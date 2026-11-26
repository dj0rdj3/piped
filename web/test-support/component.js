import { readFile } from 'node:fs/promises';
import { parse, compileScript } from 'vue/compiler-sfc';
import * as Vue from 'vue';

// Exercise the actual script-setup code with Vue's lifecycle and reactivity.
// Child rendering is covered by template tests and browser regression checks.
export async function mountComponent(
  filename,
  { props = {}, request, browser = {}, globals = {}, prepare } = {},
) {
  const url = new URL(filename, new URL('../src/', import.meta.url));
  const { descriptor } = parse(await readFile(url, 'utf8'), { filename: url.pathname });
  const compiled = compileScript(descriptor, { id: 'regression', genDefaultAs: 'component' });
  const bindings = {
    __PIPED_DEVELOPMENT__: false,
    window: Object.assign(Object.create(globalThis), browser),
    ...globals,
  };
  for (const [name, entry] of Object.entries(compiled.imports)) {
    if (entry.source.endsWith('.vue')) {
      bindings[name] = {};
    } else if (entry.source.endsWith('/api.js')) {
      bindings[name] = request;
    } else {
      const module = entry.source.startsWith('.')
        ? await import(new URL(entry.source, url))
        : await import(entry.source);
      bindings[name] = module[entry.imported];
    }
  }
  const source = compiled.content.replace(/^import\s[\s\S]*?from\s*['"][^'"]+['"];?\s*$/gm, '');
  const component = new Function(...Object.keys(bindings), `${source}\nreturn component;`)(
    ...Object.values(bindings),
  );
  return mountDefinition(component, { props, prepare });
}

export function mountModel(factory, { props = {}, request, browser = {}, prepare } = {}) {
  const events = [];
  const component = {
    setup() {
      return factory(
        props,
        (...args) => events.push(args),
        request,
        Object.assign(Object.create(globalThis), browser),
      );
    },
  };
  return { ...mountDefinition(component, { props, prepare }), events };
}

function mountDefinition(component, { props, prepare }) {
  const events = [];
  const listeners = Object.fromEntries(
    (component.emits ?? []).map((name) => [
      'on' + name.replace(/(^|-)([a-z])/g, (_match, _separator, letter) => letter.toUpperCase()),
      (...args) => events.push([name, ...args]),
    ]),
  );
  let state;
  const renderer = Vue.createRenderer({
    createComment: () => ({}),
    insert() {},
    remove() {},
    parentNode: () => null,
    nextSibling: () => null,
  });
  const app = renderer.createApp(
    {
      ...component,
      setup(props, context) {
        state = component.setup(props, context);
        prepare?.(state);
        return state;
      },
      render: () => null,
    },
    { ...props, ...listeners },
  );
  app.mount({});
  return { state: Vue.proxyRefs(state), events, unmount: () => app.unmount() };
}

export function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

export async function settle() {
  for (let tick = 0; tick < 12; tick++) {
    await Promise.resolve();
  }
  await Vue.nextTick();
}
