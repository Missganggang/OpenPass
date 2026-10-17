#!/usr/bin/env node
'use strict';

// Run the real admin view in a small DOM/fetch harness. No router is contacted.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../../web/app.js'), 'utf8');
const init = source.lastIndexOf('\n  setupActionDialogEvents();');
assert.ok(init > 0);
const testedSource = source.slice(0, init) + '\n globalThis.ui = { state, isL2TP, regularNodes, nodeLabel, deviceModeSelect, renderNodes, renderL2TP, refreshL2TP, syncL2TPPolling, openL2TPForm, closeL2TPForm, saveL2TP, runL2TPAction, finishActionDialog };\n})();';

function create() {
  const nodes = new Map(), requests = [], intervals = new Map(), copied = [];
  let nextInterval = 0;
  const document = { hidden: false, activeElement: null };
  function element(selector) {
    if (nodes.has(selector)) return nodes.get(selector);
    const classes = new Set(String(selector).includes('Backdrop') ? ['hidden'] : []);
    const node = {
      value: '', innerHTML: '', textContent: '', disabled: false, required: false, isConnected: true, children: [], style: {}, attrs: {},
      classList: { contains: name => classes.has(name), add: name => classes.add(name), remove: name => classes.delete(name), toggle: (name, enabled) => enabled ? classes.add(name) : classes.delete(name) },
      setAttribute(name, value) { this.attrs[name] = value; }, removeAttribute(name) { delete this.attrs[name]; },
      addEventListener() {}, focus() { document.activeElement = this; }, select() {}, reportValidity() { return true; },
      appendChild(child) { this.children.push(child); }, remove() { this.removed = true; }, querySelector: selector => element(selector), querySelectorAll: () => []
    };
    nodes.set(selector, node);
    return node;
  }
  Object.assign(document, { querySelector: element, querySelectorAll: () => [], createElement: () => element(Symbol()), addEventListener() {}, body: element('body') });
  const app = { nodes, requests, intervals, copied, el: element, response: () => ({ connections: [], dependencies: { available: true, missing: [] } }) };
  const context = vm.createContext({
    document, location: { pathname: '/', origin: 'http://10.0.0.1:18080', hash: '' }, window: {},
    navigator: { clipboard: { writeText: async text => copied.push(text) } },
    setTimeout() {}, setInterval(callback, delay) { const id = ++nextInterval; intervals.set(id, { callback, delay }); return id; }, clearInterval: id => intervals.delete(id),
    fetch: async (url, options = {}) => {
      const request = { url, method: options.method || 'GET', body: options.body ? JSON.parse(options.body) : undefined };
      requests.push(request);
      const result = await app.response(request);
      if (result instanceof Error) throw result;
      const status = result?.httpStatus || 200;
      return { ok: status < 400, status, text: async () => JSON.stringify(result?.httpStatus ? result.data : result) };
    }
  });
  vm.runInContext(testedSource, context, { filename: 'web/app.js' });
  app.ui = context.ui;
  app.document = document;
  app.toasts = () => element('#toastStack').children.map(node => node.textContent);
  return app;
}
const tick = () => new Promise(resolve => setImmediate(resolve));
const connection = overrides => ({ id: 'l2tp-a', type: 'l2tp', name: 'Office', address: 'vpn.example.test', username: 'person', remark: '香港办公', enabled: false, mtu: 1400, bound_devices: 0, status: { state: 'disconnected', connected: false }, ...overrides });

(async () => {
  let app = create();
  const { state } = app.ui;
  state.nodes = [{ id: 'socks', type: 'socks5', name: 'SOCKS' }, connection({ enabled: true }), connection({ id: 'draft', name: 'Draft' })];
  assert.deepEqual(Array.from(app.ui.regularNodes(), node => node.id), ['socks']);
  const options = app.ui.deviceModeSelect({ id: 'device', mode: 'proxy', node_id: 'l2tp-a' });
  assert.match(options, /\[L2TP\].*香港办公/);
  assert.doesNotMatch(options, /Draft/);
  app.ui.renderNodes();
  assert.match(app.el('#nodesTable').innerHTML, /SOCKS/);
  assert.doesNotMatch(app.el('#nodesTable').innerHTML, /Office/);

  // Enabled does not mean online: only netifd's confirmed state changes the badge.
  state.l2tpConnections = [connection({ enabled: true })];
  app.ui.renderL2TP();
  assert.match(app.el('#l2tpList').innerHTML, /已断开/);
  assert.doesNotMatch(app.el('#l2tpList').innerHTML, /l2tp-card-online/);
  state.l2tpConnections[0].status = { state: 'connected', connected: true, device: 'l2tp-opl2a', local_ip: '10.3.0.2', uptime: 3660 };
  app.ui.renderL2TP();
  assert.match(app.el('#l2tpList').innerHTML, /l2tp-card-online/);
  assert.match(app.el('#l2tpList').innerHTML, /1 小时 1 分钟/);
  assert.match(app.el('#l2tpList').innerHTML, /10\.3\.0\.2/);
  state.l2tpConnections[0].name = '<script>bad()</script>';
  app.ui.renderL2TP();
  assert.doesNotMatch(app.el('#l2tpList').innerHTML, /<script>/);
  app.response = () => new Error('Backend unavailable');
  await app.ui.refreshL2TP(true);
  assert.match(app.el('#l2tpList').innerHTML, /状态待确认/);
  assert.doesNotMatch(app.el('#l2tpList').innerHTML, /l2tp-card-online/);
  assert.match(app.toasts().at(-1), /刷新失败/);

  app = create();
  app.response = () => ({ connections: [connection()], dependencies: { available: false, missing: ['xl2tpd', 'pppd'] } });
  await app.ui.refreshL2TP(true);
  assert.match(app.el('#l2tpDependencies').textContent, /xl2tpd、pppd/);
  assert.match(app.el('#l2tpList').innerHTML, /data-l2tp-action="connect"[^>]*disabled/);
  assert.match(app.toasts().at(-1), /状态已刷新/);

  // Poll only while this page is visible, and never overlap a pending read.
  app = create();
  app.ui.syncL2TPPolling();
  assert.equal(app.intervals.size, 0);
  app.ui.state.page = 'l2tp';
  let resolveRead;
  app.response = () => new Promise(resolve => { resolveRead = resolve; });
  app.ui.syncL2TPPolling();
  assert.equal(app.intervals.size, 1);
  assert.equal([...app.intervals.values()][0].delay, 5000);
  const concurrent = app.ui.refreshL2TP();
  assert.equal(app.requests.length, 1);
  resolveRead({ connections: [], dependencies: { available: true } });
  await concurrent; await tick();
  app.document.hidden = true; app.ui.syncL2TPPolling();
  assert.equal(app.intervals.size, 0);
  app.document.hidden = false; app.ui.state.page = 'devices'; app.ui.syncL2TPPolling();
  assert.equal(app.intervals.size, 0);

  // Creating a connection cannot implicitly start it.
  app = create();
  app.ui.openL2TPForm();
  app.el('#l2tpName').value = 'Office'; app.el('#l2tpAddress').value = 'vpn.example.test';
  app.el('#l2tpUsername').value = 'user'; app.el('#l2tpPassword').value = ' secret ';
  await app.ui.saveL2TP({ preventDefault() {} });
  const created = app.requests.find(request => request.method === 'POST');
  assert.equal(created.url, '/api/l2tp');
  assert.equal(created.body.enabled, false);
  assert.equal(created.body.password, ' secret ', 'Do not trim authentication passwords');
  assert.equal(created.body.mtu, 1400);
  assert.equal(app.el('#l2tpPassword').value, '', 'Clear form credentials after saving');

  // Editing a remark leaves the stored password and connection lifecycle unchanged.
  app.ui.state.l2tpConnections = [connection({ enabled: true })];
  app.ui.openL2TPForm('l2tp-a');
  app.el('#l2tpRemark').value = 'Updated note';
  await app.ui.saveL2TP({ preventDefault() {} });
  const edited = app.requests.find(request => request.method === 'PATCH');
  assert.equal(edited.body.remark, 'Updated note');
  assert.equal('password' in edited.body, false);
  assert.equal('enabled' in edited.body, false);

  // Disconnect requires confirmation and does not touch device bindings.
  app = create(); app.ui.state.l2tpConnections = [connection({ enabled: true, bound_devices: 2 })];
  let pending = app.ui.runL2TPAction('disconnect', 'l2tp-a');
  assert.match(app.el('#actionDialogMessage').textContent, /不会回退到本地直连/);
  app.ui.finishActionDialog(null); await pending;
  assert.equal(app.requests.length, 0);
  pending = app.ui.runL2TPAction('disconnect', 'l2tp-a');
  app.ui.finishActionDialog(true); await pending;
  assert.equal(app.requests.find(request => request.method === 'POST').url, '/api/l2tp/l2tp-a/disconnect');
  assert.ok(app.requests.every(request => !request.url.includes('/devices')));

  // Connecting also enables global protection on the server; the other pages
  // must reflect its current settings and kernel state without a manual reload.
  for (const action of ['connect', 'reconnect']) {
    app = create(); app.ui.state.l2tpConnections = [connection()];
    app.ui.state.settings = { enabled: false }; app.ui.state.status = { enabled: false, kernel_running: false };
    app.response = request => request.url === '/api/settings' ? { enabled: true, default_dns: 'aliyun' }
      : request.url === '/api/status' ? { enabled: true, kernel_running: true, version: '0.1.13' }
      : { connections: [connection({ enabled: true, status: { state: 'connecting' } })], dependencies: { available: true } };
    pending = app.ui.runL2TPAction(action, 'l2tp-a');
    if (action === 'reconnect') app.ui.finishActionDialog(true);
    await pending;
    assert.equal(app.ui.state.settings.enabled, true);
    assert.equal(app.ui.state.status.kernel_running, true);
    assert.equal(app.el('#deviceRuntimeNotice').textContent, '');
    assert.match(app.el('#quickProtect').innerHTML, /全局保护已开启/);
    assert.ok(app.requests.some(request => request.url === '/api/settings'));
    assert.ok(app.requests.some(request => request.url === '/api/status'));
  }

  app = create(); app.ui.state.l2tpConnections = [connection()];
  app.response = request => request.url === '/api/status' ? new Error('Status temporarily unavailable') : { connections: [connection()], dependencies: { available: true } };
  await app.ui.runL2TPAction('connect', 'l2tp-a');
  assert.match(app.toasts().at(-1), /已发起 L2TP 连接.*全局状态刷新失败/);
  assert.ok(app.toasts().every(text => !text.includes('操作失败')), 'A successful request with a failed refresh is not a failed connection request');

  // Offline and hidden bindings must also block deletion.
  app = create(); app.ui.state.l2tpConnections = [connection()];
  app.response = () => ({ devices: [{ id: 'phone', node_id: 'l2tp-a', hostname: 'Phone', ip: '10.0.0.42', online: false, hidden: true }] });
  pending = app.ui.runL2TPAction('delete', 'l2tp-a'); await tick();
  assert.match(app.el('#actionDialogMessage').textContent, /离线、隐藏设备/);
  app.ui.finishActionDialog(true); await pending;
  assert.ok(app.requests.every(request => request.method !== 'DELETE'));

  // A server-side binding race is reported without claiming that deletion succeeded.
  app = create(); app.ui.state.l2tpConnections = [connection()];
  app.response = request => request.url.includes('/devices') ? { devices: [] } : request.method === 'DELETE' ? { httpStatus: 409, data: { error: '请先解绑设备', bound_devices: [{ hostname: 'New phone' }] } } : { connections: [connection()], dependencies: { available: true } };
  pending = app.ui.runL2TPAction('delete', 'l2tp-a'); await tick();
  app.ui.finishActionDialog(true); await tick();
  assert.match(app.el('#actionDialogMessage').textContent, /New phone/);
  app.ui.finishActionDialog(true); await pending;
  assert.ok(app.toasts().every(text => !text.includes('已删除')));

  // Connection information is shareable without exposing the username or password.
  app = create(); app.ui.state.l2tpConnections = [connection({ username: 'private-user', password: 'private-secret' })];
  await app.ui.runL2TPAction('copy', 'l2tp-a');
  assert.match(app.copied[0], /vpn\.example\.test/);
  assert.doesNotMatch(app.copied[0], /private-user|private-secret/);
  console.log('L2TP admin UI tests passed');
})().catch(error => { console.error(error); process.exitCode = 1; });
