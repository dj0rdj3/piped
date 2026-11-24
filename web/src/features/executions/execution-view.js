// Store only public selection IDs. Terminal contents, input drafts, credentials,
// and session tokens never enter browser persistence.
function validID(value) {
  return typeof value === 'string' && /^[a-f0-9]{32}$/.test(value);
}

export function readExecutionView(browser, userID) {
  try {
    const view = JSON.parse(browser.localStorage.getItem(`piped.execution-view.${userID}`));
    if (!validID(view?.jobID)) {
      return null;
    }
    return {
      jobID: view.jobID,
      runID: validID(view.runID) ? view.runID : '',
      hostID: validID(view.hostID) ? view.hostID : '',
    };
  } catch {
    return null;
  }
}

export function writeExecutionView(browser, userID, view) {
  try {
    const key = `piped.execution-view.${userID}`;
    if (!view || !validID(view.jobID)) {
      browser.localStorage.removeItem(key);
      return;
    }
    const serialized = JSON.stringify({
      jobID: view.jobID,
      runID: validID(view.runID) ? view.runID : '',
      hostID: validID(view.hostID) ? view.hostID : '',
    });
    if (browser.localStorage.getItem(key) !== serialized) {
      browser.localStorage.setItem(key, serialized);
    }
  } catch {
    // Browser privacy settings may disable storage; execution still works.
  }
}
