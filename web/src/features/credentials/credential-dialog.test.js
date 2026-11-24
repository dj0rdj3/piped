import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import { compile, createRenderer } from 'vue';

// Compile the actual templates, then inspect their VNodes in a Vue render
// context. This exercises event modifiers without a DOM shim or test dependency.
async function templateRenderer(filename, bindings) {
  const source = await readFile(new URL(filename, import.meta.url), 'utf8');
  const start = source.indexOf('<template>') + '<template>'.length;
  const template = source.slice(start, source.lastIndexOf('</template>'));
  const bindingMetadata = Object.fromEntries(
    Object.keys(bindings).map((name) => [name, 'setup-const']),
  );
  const render = compile(template, { bindingMetadata });

  return (context) => {
    const renderer = createRenderer({
      createComment: () => ({}),
      insert() {},
      remove() {},
      parentNode: () => null,
      nextSibling: () => null,
    });
    let vnode;
    let files;
    const app = renderer.createApp({
      render() {
        vnode = render(context, [], {}, bindings);
        files = findFiles(vnode);
        return null;
      },
    });
    app.mount({});
    app.unmount();
    return { vnode, files };
  };
}

function findFiles(vnode) {
  if (Array.isArray(vnode)) {
    return vnode.flatMap(findFiles);
  }
  if (!vnode) {
    return [];
  }

  const found = vnode.type === 'input' && vnode.props.type === 'file' ? [vnode] : [];
  if (Array.isArray(vnode.children)) {
    found.push(...findFiles(vnode.children));
  } else if (vnode.children?.default) {
    found.push(...findFiles(vnode.children.default()));
  }
  return found;
}

const renderModal = await templateRenderer('../../components/ui/AppDialog.vue', {
  AppButton: 'button',
  X: 'svg',
});
const renderImport = await templateRenderer('./CredentialDialog.vue', {
  AppDialog: 'dialog',
  AppButton: 'button',
  FormInput: 'input',
  StatusMessage: 'div',
  Check: 'svg',
  Copy: 'svg',
  KeyRound: 'svg',
  LoaderCircle: 'svg',
});

function renderedImport() {
  return renderImport({
    mode: 'import',
    dirty: false,
    formDialog: null,
    currentMode: { title: 'Import SSH key', action: 'Import key' },
    busy: false,
    readingFiles: 0,
    error: '',
    name: '',
    privateKey: '',
    publicKey: '',
    passphrase: '',
    emit() {},
    submit() {},
    loadKeyFile() {},
    copyPublicKey() {},
  });
}

function renderedModal(requestClose) {
  return renderModal({
    dialog: null,
    title: 'Import SSH key',
    titleId: 'test-title',
    busy: false,
    wide: true,
    requestClose,
    $slots: {},
  }).vnode;
}

test('file picker cancellation cannot close the enclosing credential dialog', () => {
  let closed = 0;
  const modal = renderedModal(() => {
    closed++;
  });
  const { files } = renderedImport();
  assert.equal(files.length, 2);

  for (const file of files) {
    let stopped = false;
    let prevented = false;
    const event = {
      target: file,
      currentTarget: modal,
      stopPropagation() {
        stopped = true;
      },
      preventDefault() {
        prevented = true;
      },
    };
    file.props.onCancel(event);
    assert.equal(stopped, true);

    // The shared modal also ignores any descendant's cancel event.
    modal.props.onCancel(event);
    assert.equal(closed, 0);
    assert.equal(prevented, false);
  }
});

test('Escape on the modal itself still invokes its close guard', () => {
  let closed = 0;
  const modal = renderedModal(() => {
    closed++;
  });
  let prevented = false;
  modal.props.onCancel({
    target: modal,
    currentTarget: modal,
    preventDefault() {
      prevented = true;
    },
  });
  assert.equal(closed, 1);
  assert.equal(prevented, true);
});
