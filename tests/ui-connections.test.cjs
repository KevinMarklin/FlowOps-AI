const { readFileSync } = require('node:fs');
const { resolve } = require('node:path');
const { runInNewContext } = require('node:vm');
const test = require('node:test');
const assert = require('node:assert/strict');
const source = readFileSync(resolve(__dirname, '../web/app.js'), 'utf8');
function fn(name) {
  const start = source.search(new RegExp(`^  (?:async )?function ${name}\\(`, 'm'));
  assert.ok(start >= 0);
  const rest = source.slice(start), end = rest.slice(1).search(/\n  (?:async )?function /);
  return end < 0 ? rest : rest.slice(0, end + 1);
}
function harness(extra = {}) {
  const input = { value: '' }, link = { hidden: true, removeAttribute() { delete this.href; } };
  const expiry = {}, box = { hidden: true }, button = { disabled: false };
  const dialog = { open: true }, error = { hidden: true }, row = { dataset: { personalChannel: 'telegram' } };
  const context = {
    settingsEpoch: 1, URL, Date, hasSession: () => true,
    loadPersonalLinks() {},
    $: (selector) => ({ '#user-settings': dialog, '#user-settings-error': error, '[data-personal-code]': box, input, '[data-open-bot]': link, '[data-code-expiry]': expiry }[selector]),
    $$: () => [button], ...extra,
  };
  return { context, row, input, link, box, dialog, run(name) { runInNewContext(`${fn(name)}\nthis.invoke=${name}`, context); return context.invoke; } };
}
test('closed and reopened settings ignore an older status response', async () => {
  let complete;
  const h = harness({ api: () => new Promise(resolve => { complete = resolve; }), $$: () => { throw new Error('stale response touched controls'); } });
  const task = h.run('loadPersonalLinks')();
  h.context.settingsEpoch++;
  complete({ can_connect: true, channels: {} });
  await task;
});
test('connection action is not submitted twice while its request is pending', async () => {
  let complete, calls = 0;
  const h = harness({ api: () => { calls++; return new Promise(resolve => { complete = resolve; }); } });
  const change = h.run('changePersonalLink');
  const pending = change(h.row, false);
  await change(h.row, false);
  assert.equal(calls, 1);
  complete({ command: '/link synthetic', expires_at: new Date().toISOString(), open_url: 'https://t.me/FlowOps_bot?start=synthetic' });
  await pending;
  assert.equal(h.link.hidden, false);
  assert.equal(h.row.dataset.busy, undefined);
});
test('unsafe bot URLs are rejected and late-issued codes stay hidden', async () => {
  const h = harness({ api: async () => ({ command: '/link synthetic', expires_at: new Date().toISOString(), open_url: 'https://attacker.example/?start=synthetic' }) });
  await h.run('changePersonalLink')(h.row, false);
  assert.equal(h.link.hidden, true);
  assert.equal(h.link.href, undefined);
  let complete;
  const late = harness({ api: () => new Promise(resolve => { complete = resolve; }) });
  const task = late.run('changePersonalLink')(late.row, false);
  late.context.settingsEpoch++;
  complete({ command: '/link must-not-display', expires_at: new Date().toISOString() });
  await task;
  assert.equal(late.input.value, '');
  assert.equal(late.box.hidden, true);
});

test('work heartbeat does not reset the operator inactivity clock', async () => {
  const context = { state: { user: { role: 'operator' } }, sessionGeneration: 1,
    workState: { id: '42', running: true, busy: false, activity: 123, seconds: 0 },
    hasSession: () => true, api: async () => ({ active_seconds: 15, running: true }), renderWork() {}, toast() {}, Date };
  runInNewContext(`${fn('trackWork')}\nthis.track = trackWork;`, context);
  assert.equal(await context.track('42', 'heartbeat'), true);
  assert.equal(context.workState.activity, 123);
});

test('late work response cannot replace another account work state', async () => {
  let complete;
  const context = { state: { user: { role: 'operator' } }, sessionGeneration: 1,
    workState: { id: '42', running: true, busy: false, activity: 123, seconds: 0 },
    hasSession: () => true, api: () => new Promise(resolve => { complete = resolve; }), renderWork() {}, toast() {}, Date };
  runInNewContext(`${fn('trackWork')}\nthis.track = trackWork;`, context);
  const pending = context.track('42', 'start');
  context.sessionGeneration++;
  context.workState = { id: null, running: false, busy: false, activity: 0, seconds: 0 };
  complete({ active_seconds: 999, running: true });
  assert.equal(await pending, false);
  assert.equal(context.workState.id, null);
  assert.equal(context.workState.seconds, 0);
});
