import { useJobDraft } from './useJobDraft.js';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mountModel, mountComponent, deferred, settle } from '../../../test-support/component.js';

const host = { id: 'host-one', name: 'Host', tags: [] };
const job = {
  id: 'job-one',
  name: 'Job',
  version: 2,
  script: 'printf hello',
  host_ids: [host.id],
  secret_names: ['TOKEN'],
  schedule: { cron: '0 2 * * *', enabled: true },
};

test('job start retries retain one identity and successful starts clear it', async (t) => {
  const writes = [];
  let fail = true;
  const target = { ...job };
  const fixture = await mountComponent('features/jobs/JobsPage.vue', {
    props: { currentUserId: 1 },
    browser: { setInterval: () => 1, clearInterval() {} },
    globals: { document: { visibilityState: 'visible' } },
    request(path, options = {}) {
      if (options.method === 'POST') {
        writes.push(options.body);
        if (fail) {
          return Promise.reject(Object.assign(new Error('Acknowledgement lost'), { status: 0 }));
        }
        return Promise.resolve({
          run: { id: 'same-admitted-run', status: 'running', started_at: 1, hosts: [] },
        });
      }
      return Promise.resolve(path === '/hosts' ? { hosts: [host] } : { jobs: [target] });
    },
  });
  t.after(fixture.unmount);
  await settle();
  await fixture.state.start(target);
  assert.equal(writes.length, 1);
  assert.match(writes[0].request_id, /^[a-f0-9-]{36}$/);
  fail = false;
  await fixture.state.start(target);
  assert.equal(writes[1].request_id, writes[0].request_id);
  assert.equal(fixture.state.execution.run.id, 'same-admitted-run');
  await fixture.state.start(target);
  assert.notEqual(writes[2].request_id, writes[0].request_id);
});

test('leaving Jobs aborts start requests and ignores late success or sign-out', async (t) => {
  for (const outcome of ['success', 'unauthorized']) {
    const pending = deferred();
    let signal;
    const target = { ...job };
    const fixture = await mountComponent('features/jobs/JobsPage.vue', {
      props: { currentUserId: 1 },
      browser: { setInterval: () => 1, clearInterval() {} },
      globals: { document: { visibilityState: 'visible' } },
      request(path, options = {}) {
        if (options.method === 'POST') {
          signal = options.signal;
          return pending.promise;
        }
        return Promise.resolve(path === '/hosts' ? { hosts: [host] } : { jobs: [target] });
      },
    });
    t.after(fixture.unmount);
    await settle();
    const starting = fixture.state.start(target);
    fixture.unmount();
    assert.equal(signal.aborted, true);
    if (outcome === 'success') {
      pending.resolve({ run: { id: 'late', hosts: [] } });
    } else {
      pending.reject(Object.assign(new Error('Expired'), { status: 401 }));
    }
    await starting;
    assert.equal(fixture.state.execution, null);
    assert.deepEqual(fixture.events, []);
  }
});

test('Jobs background refreshes cannot overwrite newer summaries or emit a stale sign-out', async (t) => {
  const summaries = [];
  let initialized = false;
  const fixture = await mountComponent('features/jobs/JobsPage.vue', {
    props: { currentUserId: 1 },
    browser: { setInterval: () => 1, clearInterval() {} },
    globals: { document: { visibilityState: 'visible' } },
    request(path) {
      if (path === '/notifications') {
        return Promise.resolve({ targets: [] });
      }
      if (path === '/hosts') {
        return Promise.resolve({ hosts: [host] });
      }
      if (!initialized) {
        initialized = true;
        return Promise.resolve({ jobs: [job] });
      }
      const response = deferred();
      summaries.push(response);
      return response.promise;
    },
  });
  t.after(fixture.unmount);
  await settle();
  const old = fixture.state.refreshSummaries();
  const latest = fixture.state.refreshSummaries();
  summaries[1].resolve({ jobs: [{ ...job, name: 'Latest' }] });
  await latest;
  summaries[0].reject(Object.assign(new Error('Expired'), { status: 401 }));
  await old;
  assert.equal(fixture.state.jobs[0].name, 'Latest');
  assert.deepEqual(fixture.events, []);
});

test('job duplication preserves stored secrets and cron but starts with scheduling disabled', async (t) => {
  const writes = [];
  const fixture = await mountModel(useJobDraft, {
    props: { mode: 'duplicate', target: job },
    browser: { setTimeout: () => 1, clearTimeout() {} },
    request(path, options) {
      if (path === '/notifications') {
        return Promise.resolve({ targets: [] });
      }
      if (!options) {
        return Promise.resolve({ hosts: [host] });
      }
      writes.push({ path, ...options });
      return Promise.resolve();
    },
  });
  t.after(fixture.unmount);
  await settle();
  assert.equal(fixture.state.canSave, true);
  await fixture.state.submit();
  assert.deepEqual(writes[0].body.copy_from, { id: job.id, version: job.version });
  assert.deepEqual(writes[0].body.secrets, [{ name: 'TOKEN' }]);
  assert.deepEqual(writes[0].body.schedule, { cron: '0 2 * * *', enabled: false });
  assert.equal(writes[0].body.version, 0);
  fixture.state.script = '\u65e5'.repeat(22000);
  await fixture.state.submit();
  assert.equal(writes.length, 1, 'UTF-8 byte limit was bypassed.');
});

test('bulk host selection searches names and tags and preserves saved execution order', async (t) => {
  const targets = [
    { id: 'database', name: 'Database', address: 'db.local', tags: ['database'] },
    { id: 'web-one', name: 'Web one', address: 'one.local', tags: ['web', 'production'] },
    { id: 'web-two', name: 'Web two', address: 'two.local', tags: ['web'] },
    { id: 'web-three', name: 'Web three', address: 'three.local', tags: ['web'] },
  ];
  const writes = [];
  const fixture = await mountModel(useJobDraft, {
    props: {
      mode: 'edit',
      target: { ...job, host_ids: ['database', 'web-two'], schedule: { cron: '', enabled: false } },
    },
    request(path, options) {
      if (path === '/notifications') {
        return Promise.resolve({ targets: [] });
      }
      if (!options) {
        return Promise.resolve({ hosts: targets });
      }
      writes.push({ path, ...options });
      return Promise.resolve();
    },
  });
  t.after(fixture.unmount);
  await settle();
  assert.deepEqual(fixture.state.hostTags, ['database', 'production', 'web']);
  assert.deepEqual(fixture.state.availableTags, ['database', 'production', 'web']);
  fixture.state.query = 'web';
  assert.deepEqual(fixture.state.availableTags, ['web']);
  fixture.state.query = 'WEB ONE';
  assert.deepEqual(fixture.state.availableTags, []);
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.hostIDs, ['database', 'web-two', 'web-one']);
  assert.equal(fixture.state.allVisibleHostsSelected, true);
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.hostIDs, ['database', 'web-two']);
  assert.equal(fixture.state.allVisibleHostsSelected, false);
  fixture.state.toggleVisibleHosts();
  fixture.state.query = 'web';
  assert.deepEqual(fixture.state.availableTags, ['web']);
  assert.equal(fixture.state.allVisibleHostsSelected, false);
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.hostIDs, ['database', 'web-two', 'web-one', 'web-three']);
  fixture.state.query = 'production';
  assert.equal(fixture.state.allVisibleHostsSelected, true);
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.hostIDs, ['database', 'web-two', 'web-three']);
  fixture.state.query = 'web';
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.hostIDs, ['database', 'web-two', 'web-three', 'web-one']);
  fixture.state.query = 'not a host';
  assert.equal(fixture.state.availableHosts.length, 0);
  assert.equal(fixture.state.allVisibleHostsSelected, false);
  fixture.state.toggleVisibleHosts();
  assert.equal(fixture.state.hostIDs.length, 4);
  fixture.state.query = '';
  assert.equal(fixture.state.allVisibleHostsSelected, true);
  fixture.state.moveHost('web-three', -1);
  await fixture.state.submit();
  assert.deepEqual(writes[0].body.host_ids, ['database', 'web-three', 'web-two', 'web-one']);
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.hostIDs, []);
  assert.equal(fixture.state.allVisibleHostsSelected, false);
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(
    fixture.state.hostIDs,
    targets.map((target) => target.id),
  );
});

test('unfiltered deselection clears targets while filtered deselection preserves tags and hidden hosts', async (t) => {
  const targets = [
    { id: 'database', name: 'Database', tags: ['database'] },
    { id: 'web-one', name: 'Web one', tags: ['web', 'production'] },
    { id: 'web-two', name: 'Web two', tags: ['web'] },
    { id: 'web-three', name: 'Web three', tags: ['web'] },
    { id: 'production-only', name: 'Production', tags: ['production', 'keep'] },
  ];
  const writes = [];
  const fixture = await mountModel(useJobDraft, {
    props: {
      mode: 'edit',
      target: {
        ...job,
        host_ids: ['database'],
        host_tags: ['web'],
        schedule: { cron: '', enabled: false },
      },
    },
    request(path, options) {
      if (!options) {
        return Promise.resolve(path === '/notifications' ? { targets: [] } : { hosts: targets });
      }
      writes.push(options.body);
      return Promise.resolve();
    },
  });
  t.after(fixture.unmount);
  await settle();
  assert.equal(fixture.state.allVisibleHostsSelected, false);
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.hostIDs, ['database', 'production-only']);
  assert.deepEqual(fixture.state.targetTags, ['web']);
  assert.equal(fixture.state.allVisibleHostsSelected, true);
  fixture.state.targetTags.push('future');
  fixture.state.excludedHostIDs = ['unrelated-exclusion'];
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.hostIDs, []);
  assert.deepEqual(fixture.state.targetTags, []);
  assert.deepEqual(fixture.state.excludedHostIDs, []);

  fixture.state.hostIDs = ['database', 'web-two'];
  fixture.state.excludedHostIDs = [];
  fixture.state.targetTags = ['web', 'production', 'keep', 'future'];
  fixture.state.query = 'web one';
  assert.equal(fixture.state.allVisibleHostsSelected, true);
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.hostIDs, ['database', 'web-two']);
  assert.deepEqual(fixture.state.targetTags, ['web', 'production', 'keep', 'future']);
  assert.deepEqual(fixture.state.excludedHostIDs, ['web-one']);
  assert.equal(fixture.state.allVisibleHostsSelected, false);
  assert.equal(fixture.state.includedHostIDs.has('web-one'), false);
  assert.equal(fixture.state.includedHostIDs.has('web-three'), true);
  assert.equal(fixture.state.includedHostIDs.has('production-only'), true);
  fixture.state.query = 'no matches';
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.hostIDs, ['database', 'web-two']);
  assert.deepEqual(fixture.state.targetTags, ['web', 'production', 'keep', 'future']);
  fixture.state.query = 'web one';
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.excludedHostIDs, []);
  assert.deepEqual(fixture.state.hostIDs, ['database', 'web-two']);
  fixture.state.toggleVisibleHosts();
  await fixture.state.submit();
  assert.deepEqual(writes[0].host_ids, ['database', 'web-two']);
  assert.deepEqual(writes[0].host_tags, ['web', 'production', 'keep', 'future']);
  assert.deepEqual(writes[0].excluded_host_ids, ['web-one']);

  fixture.state.hostIDs = ['database'];
  fixture.state.targetTags = ['web'];
  fixture.state.excludedHostIDs = [];
  fixture.state.query = 'web';
  assert.equal(fixture.state.allVisibleHostsSelected, true);
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.hostIDs, ['database']);
  assert.deepEqual(fixture.state.targetTags, []);
  assert.deepEqual(fixture.state.excludedHostIDs, []);
});

test('unchecking a tag-included host removes only that host and saves the preserved selections', async (t) => {
  for (const individuallySelected of [false, true]) {
    const writes = [];
    const fixture = await mountModel(useJobDraft, {
      props: {
        mode: 'edit',
        target: {
          ...job,
          host_ids: individuallySelected ? ['database', 'web-one'] : ['database'],
          host_tags: ['web', 'production', 'keep', 'future'],
          schedule: { cron: '', enabled: false },
        },
      },
      request(path, options) {
        if (options) {
          writes.push(options.body);
          return Promise.resolve();
        }
        return Promise.resolve(
          path === '/notifications'
            ? { targets: [] }
            : {
                hosts: [
                  { id: 'database', name: 'Database', tags: [] },
                  { id: 'web-one', name: 'Web one', tags: ['web', 'production'] },
                  { id: 'web-two', name: 'Web two', tags: ['web'] },
                  { id: 'production-only', name: 'Production', tags: ['production', 'keep'] },
                ],
              },
        );
      },
    });
    t.after(fixture.unmount);
    await settle();
    fixture.state.query = 'web one';
    fixture.state.toggleHost('web-one');
    assert.deepEqual(fixture.state.hostIDs, ['database']);
    assert.deepEqual(fixture.state.targetTags, ['web', 'production', 'keep', 'future']);
    assert.deepEqual(fixture.state.excludedHostIDs, ['web-one']);
    assert.equal(fixture.state.includedHostIDs.has('web-one'), false);
    assert.equal(fixture.state.includedHostIDs.has('web-two'), true);
    assert.equal(fixture.state.includedHostIDs.has('production-only'), true);
    assert.equal(fixture.state.allVisibleHostsSelected, false);
    assert.equal(fixture.state.dirty, true);
    fixture.state.toggleHost('web-one');
    assert.deepEqual(fixture.state.hostIDs, ['database']);
    assert.deepEqual(fixture.state.excludedHostIDs, []);
    assert.equal(fixture.state.allVisibleHostsSelected, true);
    fixture.state.toggleHost('web-one');
    await fixture.state.submit();
    assert.deepEqual(writes[0].host_ids, ['database']);
    assert.deepEqual(writes[0].host_tags, ['web', 'production', 'keep', 'future']);
    assert.deepEqual(writes[0].excluded_host_ids, ['web-one']);
  }
});

test('unchecking the final tag member deselects the tag and retains overlapping exclusions', async (t) => {
  const writes = [];
  const fixture = await mountModel(useJobDraft, {
    props: {
      mode: 'edit',
      target: {
        ...job,
        host_ids: ['database'],
        host_tags: ['web', 'production', 'future'],
        schedule: { cron: '', enabled: false },
      },
    },
    request(path, options) {
      if (options) {
        writes.push(options.body);
        return Promise.resolve();
      }
      return Promise.resolve(
        path === '/notifications'
          ? { targets: [] }
          : {
              hosts: [
                { id: 'database', name: 'Database', tags: [] },
                { id: 'web-one', name: 'Web one', tags: ['web'] },
                { id: 'web-two', name: 'Web two', tags: ['web', 'production'] },
                { id: 'production', name: 'Production worker', tags: ['production'] },
              ],
            },
      );
    },
  });
  t.after(fixture.unmount);
  await settle();
  fixture.state.toggleHost('web-one');
  assert.deepEqual(fixture.state.targetTags, ['web', 'production', 'future']);
  assert.deepEqual(fixture.state.excludedHostIDs, ['web-one']);
  fixture.state.query = 'web two';
  fixture.state.toggleHost('web-two');
  assert.deepEqual(fixture.state.hostIDs, ['database']);
  assert.deepEqual(fixture.state.targetTags, ['production', 'future']);
  assert.deepEqual(fixture.state.excludedHostIDs, ['web-two']);
  assert.equal(fixture.state.includedHostIDs.has('production'), true);
  assert.equal(fixture.state.includedHostIDs.has('web-one'), false);
  assert.equal(fixture.state.includedHostIDs.has('web-two'), false);
  fixture.state.query = 'worker';
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.targetTags, ['future']);
  assert.deepEqual(fixture.state.excludedHostIDs, []);
  assert.deepEqual(fixture.state.hostIDs, ['database']);
  await fixture.state.submit();
  assert.deepEqual(writes[0].host_tags, ['future']);
  assert.deepEqual(writes[0].excluded_host_ids, []);
});

test('selecting a tag clears its host exclusions across filters without clearing unrelated exclusions', async (t) => {
  const writes = [];
  const fixture = await mountModel(useJobDraft, {
    props: {
      mode: 'edit',
      target: {
        ...job,
        host_ids: ['direct'],
        host_tags: ['production'],
        excluded_host_ids: ['web-one', 'web-two', 'other'],
        schedule: { cron: '', enabled: false },
      },
    },
    request(path, options) {
      if (options) {
        writes.push(options.body);
        return Promise.resolve();
      }
      return Promise.resolve(
        path === '/notifications'
          ? { targets: [] }
          : {
              hosts: [
                { id: 'direct', name: 'Direct', tags: [] },
                { id: 'web-one', name: 'Web one', tags: ['web', 'production'] },
                { id: 'web-two', name: 'Web two', tags: ['web'] },
                { id: 'other', name: 'Other', tags: ['other'] },
              ],
            },
      );
    },
  });
  t.after(fixture.unmount);
  await settle();
  assert.deepEqual(fixture.state.excludedHostIDs, ['web-one', 'web-two', 'other']);
  fixture.state.query = 'not visible';
  fixture.state.toggleTag('web');
  assert.deepEqual(fixture.state.targetTags, ['production', 'web']);
  assert.deepEqual(fixture.state.excludedHostIDs, ['other']);
  assert.equal(fixture.state.includedHostIDs.has('web-one'), true);
  assert.equal(fixture.state.includedHostIDs.has('web-two'), true);
  assert.deepEqual(fixture.state.hostIDs, ['direct']);
  fixture.state.toggleTag('web');
  assert.deepEqual(fixture.state.targetTags, ['production']);
  assert.deepEqual(fixture.state.excludedHostIDs, ['other']);
  fixture.state.toggleTag('web');
  await fixture.state.submit();
  assert.deepEqual(writes[0].host_tags, ['production', 'web']);
  assert.deepEqual(writes[0].excluded_host_ids, ['other']);
});

test('partial tags reflect exclusions across filters and clicking one restores all members', async (t) => {
  const fixture = await mountModel(useJobDraft, {
    props: {
      mode: 'edit',
      target: {
        ...job,
        host_ids: [],
        host_tags: ['web', 'production', 'future'],
        excluded_host_ids: ['shared', 'other'],
        schedule: { cron: '', enabled: false },
      },
    },
    request(path) {
      return Promise.resolve(
        path === '/notifications'
          ? { targets: [] }
          : {
              hosts: [
                { id: 'shared', name: 'Shared', tags: ['web', 'production'] },
                { id: 'web', name: 'Web', tags: ['web'] },
                { id: 'production', name: 'Production', tags: ['production'] },
                { id: 'other', name: 'Other', tags: ['other'] },
              ],
            },
      );
    },
  });
  t.after(fixture.unmount);
  await settle();
  fixture.state.query = 'no matches';
  assert.deepEqual(fixture.state.partiallySelectedTags, new Set(['web', 'production']));
  fixture.state.toggleTag('web');
  assert.deepEqual(fixture.state.targetTags, ['web', 'production', 'future']);
  assert.deepEqual(fixture.state.excludedHostIDs, ['other']);
  assert.deepEqual(fixture.state.partiallySelectedTags, new Set());
  assert.equal(fixture.state.includedHostIDs.has('shared'), true);
  fixture.state.toggleTag('web');
  assert.deepEqual(fixture.state.targetTags, ['production', 'future']);
  fixture.state.excludedHostIDs.push('shared');
  assert.deepEqual(fixture.state.partiallySelectedTags, new Set(['production']));
});

test('job forms preserve dynamic tags through editing and duplication without expanding individual hosts', async (t) => {
  for (const mode of ['create', 'edit', 'duplicate']) {
    const writes = [];
    const fixture = await mountModel(useJobDraft, {
      props: {
        mode,
        target:
          mode === 'create'
            ? null
            : {
                ...job,
                host_ids: [],
                host_tags: ['saved-tag'],
                excluded_host_ids: ['excluded-host'],
                schedule: { cron: '', enabled: false },
              },
      },
      request(path, options) {
        if (path === '/hosts') {
          return Promise.resolve({ hosts: [{ ...host, tags: ['web'] }] });
        }
        if (path === '/notifications') {
          return Promise.resolve({ targets: [] });
        }
        writes.push({ path, ...options });
        return Promise.resolve();
      },
    });
    t.after(fixture.unmount);
    await settle();
    if (mode === 'create') {
      fixture.state.name = 'Tag-only job';
      fixture.state.script = 'true';
      fixture.state.targetTags = ['web'];
      assert.equal(fixture.state.dirty, true);
    } else {
      assert.ok(fixture.state.hostTags.includes('saved-tag'));
      assert.ok(fixture.state.availableTags.includes('saved-tag'));
    }
    assert.equal(fixture.state.canSave, true);
    await fixture.state.submit();
    assert.deepEqual(writes[0].body.host_ids, []);
    assert.deepEqual(writes[0].body.host_tags, mode === 'create' ? ['web'] : ['saved-tag']);
    assert.deepEqual(writes[0].body.excluded_host_ids, mode === 'create' ? [] : ['excluded-host']);
    if (mode === 'duplicate') {
      assert.deepEqual(writes[0].body.copy_from, { id: job.id, version: job.version });
    }
  }
});

test('jobs list resolves tag counts from current hosts without duplicates', async (t) => {
  const fixture = await mountComponent('features/jobs/JobsPage.vue', {
    props: { currentUserId: 1 },
    browser: { setInterval: () => 1, clearInterval() {} },
    globals: { document: { visibilityState: 'visible' } },
    request(path) {
      if (path === '/hosts') {
        return Promise.resolve({
          hosts: [
            { ...host, tags: ['web'] },
            { id: 'second', name: 'Second', tags: ['web'] },
          ],
        });
      }
      return Promise.resolve({ jobs: [{ ...job, host_tags: ['web'] }], server_timezone: 'UTC' });
    },
  });
  t.after(fixture.unmount);
  await settle();
  assert.deepEqual(fixture.state.jobHostIDs.get(job.id), [host.id, 'second']);
  fixture.state.hosts.push({ id: 'new-host', name: 'New host', tags: ['web'] });
  assert.deepEqual(fixture.state.jobHostIDs.get(job.id), [host.id, 'second', 'new-host']);
  fixture.state.hosts[1].tags = [];
  assert.deepEqual(fixture.state.jobHostIDs.get(job.id), [host.id, 'new-host']);
  fixture.state.jobs[0].excluded_host_ids = ['new-host'];
  assert.deepEqual(fixture.state.jobHostIDs.get(job.id), [host.id]);
});

test('host picker tracks tag inclusion independently of individual selection and filters', async (t) => {
  const fixture = await mountModel(useJobDraft, {
    props: { mode: 'edit', target: { ...job, host_tags: ['web'] } },
    request(path) {
      return Promise.resolve(
        path === '/notifications'
          ? { targets: [] }
          : {
              hosts: [
                { ...host, tags: ['web', 'production'] },
                { id: 'other', tags: ['Web'] },
              ],
            },
      );
    },
  });
  t.after(fixture.unmount);
  await settle();
  assert.deepEqual(fixture.state.matchingTargetTags.get(host.id), ['web']);
  assert.deepEqual(fixture.state.matchingTargetTags.get('other'), []);
  assert.equal(fixture.state.includedHostIDs.size, 1);
  fixture.state.query = 'hidden';
  fixture.state.hostIDs = [];
  fixture.state.targetTags.push('production');
  assert.deepEqual(fixture.state.matchingTargetTags.get(host.id), ['web', 'production']);
  assert.deepEqual(fixture.state.hostIDs, []);
  assert.equal(fixture.state.includedHostIDs.size, 1);
  fixture.state.excludedHostIDs = [host.id];
  assert.equal(fixture.state.includedHostIDs.size, 0);
  fixture.state.excludedHostIDs = [];
  fixture.state.hosts.push({ id: 'new-host', name: 'New host', tags: ['web'] });
  assert.equal(fixture.state.includedHostIDs.size, 2);
  assert.equal(fixture.state.availableHosts.length, 0);
  fixture.state.targetTags = [];
  assert.deepEqual(fixture.state.matchingTargetTags.get(host.id), []);
  assert.equal(fixture.state.includedHostIDs.size, 0);
  fixture.state.hostIDs = [host.id];
  fixture.state.targetTags = ['web'];
  fixture.state.targetTags = [];
  assert.deepEqual(fixture.state.hostIDs, [host.id]);
  assert.equal(fixture.state.includedHostIDs.size, 1);
});

test('Jobs cancels pending summaries when rows change and on unmount', async (t) => {
  const pending = [];
  const fixture = await mountComponent('features/jobs/JobsPage.vue', {
    props: { currentUserId: 1 },
    browser: { setInterval: () => 1, clearInterval() {} },
    globals: { document: { visibilityState: 'visible' } },
    request(path, options) {
      if (path === '/hosts') {
        return Promise.resolve({ hosts: [host] });
      }
      if (!path.includes('?ids=')) {
        return Promise.resolve({ jobs: [job] });
      }
      const response = deferred();
      pending.push({ ...response, signal: options.signal });
      return response.promise;
    },
  });
  t.after(fixture.unmount);
  await settle();
  const stale = fixture.state.refreshSummaries();
  await fixture.state.load();
  assert.equal(pending[0].signal.aborted, true);
  pending[0].reject(Object.assign(new Error('Expired'), { status: 401 }));
  await stale;
  assert.deepEqual(fixture.events, []);
  const latest = fixture.state.refreshSummaries();
  fixture.unmount();
  assert.equal(pending[1].signal.aborted, true);
  pending[1].resolve({ jobs: [{ ...job, name: 'Late replacement' }] });
  await latest;
  assert.equal(fixture.state.jobs[0].name, job.name);
});

test('host deselection preserves empty tags and case-sensitive overlapping membership', async (t) => {
  const fixture = await mountModel(useJobDraft, {
    props: {
      mode: 'edit',
      target: {
        ...job,
        host_ids: [],
        host_tags: ['web', 'Web', 'future'],
        excluded_host_ids: ['lower-one'],
      },
    },
    request(path) {
      return Promise.resolve(
        path === '/notifications'
          ? { targets: [] }
          : {
              hosts: [
                { id: 'lower-one', name: 'Lower one', tags: ['web'] },
                { id: 'shared', name: 'Shared', tags: ['web', 'Web'] },
                { id: 'upper', name: 'Upper', tags: ['Web'] },
              ],
            },
      );
    },
  });
  t.after(fixture.unmount);
  await settle();
  fixture.state.query = 'shared';
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.targetTags, ['Web', 'future']);
  assert.deepEqual(fixture.state.excludedHostIDs, ['shared']);
  assert.deepEqual([...fixture.state.includedHostIDs], ['upper']);
  fixture.state.query = 'upper';
  fixture.state.toggleVisibleHosts();
  assert.deepEqual(fixture.state.targetTags, ['future']);
  assert.deepEqual(fixture.state.excludedHostIDs, []);
  assert.equal(fixture.state.includedHostIDs.size, 0);
});
