const { readFileSync } = require('node:fs');
const { resolve } = require('node:path');
const { runInNewContext } = require('node:vm');
const test = require('node:test');
const assert = require('node:assert/strict');

const source = readFileSync(resolve(__dirname, '../web/app.js'), 'utf8');
function applicationFunction(name) {
  const start = source.search(new RegExp(`^  (?:async )?function ${name}\\(`, 'm'));
  assert.ok(start >= 0, `Missing application function: ${name}`);
  const rest = source.slice(start);
  const end = rest.slice(1).search(/\n  (?:async )?function /);
  return end < 0 ? rest : rest.slice(0, end + 1);
}
function harness(extra) {
  const context = {
    state: { user: { id: 'employee', role: 'employee' } },
    tabMemberID: 'employee', sessionGeneration: 1,
    clearLocalSession() { context.cleared = true; context.state.user = null; },
    showAuth(message) { context.message = message; },
    setAPIStatus() {}, clearWorkspaceData() {}, applyRoleUI() {}, renderChannels() {},
    $: () => null, hasSession: () => true, csrfToken: () => '',
    ...extra
  };
  return { context, run(name) {
    runInNewContext(`${applicationFunction('sessionChanged')}\n${applicationFunction(name)}\nthis.invoke = ${name};`, context);
    return context.invoke;
  }};
}
const admin = { user: { id: 'administrator', role: 'admin' } };

test('polling never silently adopts an administrator from another tab', async () => {
  const h = harness({ api: async () => admin });
  await h.run('checkAPI')();
  assert.equal(h.context.cleared, true);
  assert.equal(h.context.state.user, null);
  assert.match(h.context.message, /другой вкладке/);
});

test('activation and reload reject a cookie belonging to a different member', async () => {
  for (const expected of [undefined, { id: 'employee', role: 'employee' }]) {
    const h = harness({ fetch: async (path, options) => {
      assert.equal(options.headers['X-FlowOps-Member'], 'employee');
      return { ok: true, status: 200, json: async () => admin };
    }});
    assert.equal(await h.run('restoreSession')(expected), false);
    assert.equal(h.context.cleared, true);
    assert.equal(h.context.state.user, null);
  }
});

test('API binds the tab to its member and stops on a shared-cookie conflict', async () => {
  const h = harness({ fetch: async (path, options) => {
    assert.equal(options.headers['X-FlowOps-Member'], 'employee');
    return { status: 409, json: async () => ({ code: 'session_changed' }) };
  }});
  await assert.rejects(h.run('api')('/api/tickets'), /другой вкладке/);
  assert.equal(h.context.cleared, true);
});
