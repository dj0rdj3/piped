import assert from 'node:assert/strict';
import { test } from 'node:test';
import { reactive } from 'vue';
import { confirmDiscardChanges, formChanges, registerUnsavedChanges } from './unsaved-changes.js';

function browserHarness() {
  const listeners = new Map();
  let prompts = 0;
  return {
    listeners,
    get prompts() {
      return prompts;
    },
    answer: false,
    confirm(message) {
      assert.equal(message, 'Discard unsaved changes?');
      prompts++;
      return this.answer;
    },
    addEventListener(name, listener) {
      listeners.set(name, listener);
    },
    removeEventListener(name, listener) {
      if (listeners.get(name) === listener) {
        listeners.delete(name);
      }
    },
  };
}

test('form changes detect edits, host reordering, secret removal, and reverting', () => {
  const form = reactive({
    name: 'Original',
    hosts: ['one', 'two'],
    secrets: [{ name: 'TOKEN', value: '' }],
  });
  const dirty = formChanges(() => form);
  assert.equal(dirty.value, false);
  form.name = 'Edited';
  assert.equal(dirty.value, true);
  form.name = 'Original';
  assert.equal(dirty.value, false);
  form.hosts.reverse();
  assert.equal(dirty.value, true);
  form.hosts.reverse();
  assert.equal(dirty.value, false);
  form.secrets[0].value = 'typed secret';
  assert.equal(dirty.value, true);
  form.secrets[0].value = '';
  assert.equal(dirty.value, false);
  const removed = form.secrets.pop();
  assert.equal(dirty.value, true);
  form.secrets.push(removed);
  assert.equal(dirty.value, false);
});

test('discard and unload guards protect dirty forms and unregister on close', () => {
  const browser = browserHarness();
  let dirty = false;
  const unregister = registerUnsavedChanges(() => dirty, browser);
  try {
    assert.equal(confirmDiscardChanges(browser), true);
    assert.equal(browser.prompts, 0);
    let prevented = false;
    const event = {
      preventDefault() {
        prevented = true;
      },
    };
    browser.listeners.get('beforeunload')(event);
    assert.equal(prevented, false);
    dirty = true;
    assert.equal(confirmDiscardChanges(browser), false);
    assert.equal(browser.prompts, 1);
    browser.answer = true;
    assert.equal(confirmDiscardChanges(browser), true);
    browser.listeners.get('beforeunload')(event);
    assert.equal(prevented, true);
    assert.equal(event.returnValue, '');
  } finally {
    unregister();
  }
  assert.equal(browser.listeners.size, 0);
  browser.answer = false;
  assert.equal(confirmDiscardChanges(browser), true);
});
