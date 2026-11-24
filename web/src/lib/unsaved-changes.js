import { computed } from 'vue';

const guards = new Set();

// Compare form models, including controls that do not emit native form events
// (host ordering and secret removal). Snapshots stay in memory only.
export function formChanges(readState, enabled = () => true) {
  const initial = JSON.stringify(readState());
  return computed(() => enabled() && JSON.stringify(readState()) !== initial);
}

export function confirmDiscardChanges(browser = window) {
  return ![...guards].some((dirty) => dirty()) || browser.confirm('Discard unsaved changes?');
}

export function registerUnsavedChanges(dirty, browser = window) {
  guards.add(dirty);
  function beforeUnload(event) {
    if (dirty()) {
      event.preventDefault();
      event.returnValue = '';
    }
  }
  browser.addEventListener('beforeunload', beforeUnload);
  return () => {
    guards.delete(dirty);
    browser.removeEventListener('beforeunload', beforeUnload);
  };
}
