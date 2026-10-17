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
const testedSource = source.slice(0, init) + '\n globalThis.ui = { state, isL2TP, regularNodes, nodeLabel, deviceModeSelect, renderSelf, renderNodes, renderL2TP, refreshL2TP, syncL2TPPolling, toggleL2TPModule, retryL2TPModule, openL2TPForm, closeL2TPForm, toggleL2TPPassword, saveL2TP, openL2TPImport, closeL2TPImport, importL2TP, runL2TPAction, finishActionDialog, setupL2TPEvents };\n})();';

function create(storage = new Map()) {
  const nodes = new Map(), requests = [], intervals = new Map(), copied = [];
  let nextInterval = 0;
  const document = { hidden: false, activeElement: null };
  function element(selector) {
    if (nodes.has(selector)) return nodes.get(selector);
    const classes = new Set(String(selector).includes('Backdrop') ? ['hidden'] : []);
    const node = {
      value: '', innerHTML: '', textContent: '', disabled: false, required: false, isConnected: true, children: [], style: {}, attrs: {}, handlers: {},
      classList: { contains: name => classes.has(name), add: name => classes.add(name), remove: name => classes.delete(name), toggle: (name, enabled) => enabled ? classes.add(name) : classes.delete(name) },
      setAttribute(name, value) { this.attrs[name] = value; }, removeAttribute(name) { delete this.attrs[name]; },
      addEventListener(name, callback) { this.handlers[name] = callback; }, focus() { document.activeElement = this; }, select() {}, reportValidity() { return true; },
      appendChild(child) { this.children.push(child); }, remove() { this.removed = true; }, querySelector: selector => element(selector), querySelectorAll: () => []
    };
    nodes.set(selector, node);
    return node;
  }
  Object.assign(document, { querySelector: element, querySelectorAll: () => [], createElement: () => element(Symbol()), addEventListener() {}, body: element('body') });
  const app = { nodes, requests, intervals, copied, storage, el: element, response: () => ({ connections: [], dependencies: { available: true, missing: [] } }) };
  const context = vm.createContext({
    document, location: { pathname: '/', origin: 'http://10.0.0.1:18080', hash: '' }, window: {},
    sessionStorage: { getItem: key => storage.get(key) || null, setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key) },
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
  assert.equal(app.intervals.size, 0, 'Do not poll before the module state is known');
  const concurrent = app.ui.refreshL2TP();
  assert.equal(app.requests.length, 1);
  resolveRead({ connections: [], dependencies: { available: true } });
  await concurrent; await tick();
  assert.equal(app.intervals.size, 1);
  assert.equal([...app.intervals.values()][0].delay, 5000);
  app.document.hidden = true; app.ui.syncL2TPPolling();
  assert.equal(app.intervals.size, 0);

  // Closing the module stops its timer and keeps disabled connections manageable.
  app = create(); app.ui.state.page = 'l2tp';
  let enabled = true;
  app.response = request => {
    if (request.url === '/api/l2tp/settings') enabled = request.body.enabled;
    if (request.url === '/api/settings') return { enabled: true, l2tp_disabled: !enabled };
    if (request.url === '/api/status') return { enabled: true, kernel_running: true };
    return { enabled, connections: [connection({ enabled: true, status: { state: enabled ? 'connected' : 'disabled', connected: enabled } })], dependencies: enabled ? { available: true } : null };
  };
  await app.ui.refreshL2TP(); assert.equal(app.intervals.size, 1);
  let moduleAction = app.ui.toggleL2TPModule();
  assert.match(app.el('#actionDialogMessage').textContent, /已绑定 L2TP 的设备将断网/);
  app.ui.finishActionDialog(null); await moduleAction;
  assert.equal(app.requests.filter(request => request.method === 'PATCH').length, 0);
  moduleAction = app.ui.toggleL2TPModule(); app.ui.finishActionDialog(true); await moduleAction;
  assert.equal(app.ui.state.l2tpEnabled, false);
  assert.equal(app.ui.state.settings.l2tp_disabled, true);
  assert.equal(app.intervals.size, 0);
  assert.match(app.el('#l2tpRefreshTime').textContent, /自动刷新已停止/);
  assert.match(app.el('#l2tpList').innerHTML, /L2TP 已关闭/);
  assert.match(app.el('#l2tpList').innerHTML, /data-l2tp-action="reconnect"[^>]*disabled/);
  assert.doesNotMatch(app.el('#l2tpList').innerHTML, /data-l2tp-action="edit"[^>]*disabled/);
  const blockedChoices = app.ui.deviceModeSelect({ id: 'device', mode: 'proxy', node_id: 'l2tp-a' });
  assert.match(blockedChoices, /selected disabled.*L2TP 已关闭/);
  const newChoices = app.ui.deviceModeSelect({ id: 'new', mode: 'proxy' });
  assert.doesNotMatch(newChoices, /\[L2TP\]/);
  const beforeConnect = app.requests.length;
  await app.ui.runL2TPAction('connect', 'l2tp-a'); assert.equal(app.requests.length, beforeConnect);
  app.ui.syncL2TPPolling(); await tick();
  assert.equal(app.requests.length, beforeConnect + 1, 'Returning to a disabled page may read once');
  assert.equal(app.intervals.size, 0);
  await app.ui.refreshL2TP(true); assert.equal(app.intervals.size, 0, 'Manual refresh must not restore polling');
  await app.ui.toggleL2TPModule();
  assert.equal(app.ui.state.l2tpEnabled, true); assert.equal(app.intervals.size, 1);
  assert.equal(app.ui.state.settings.enabled, true); assert.equal(app.ui.state.status.kernel_running, true);
  assert.ok(app.requests.some(request => request.url === '/api/settings'));

  // A disabled module keeps the self-service binding label but removes it as a new choice.
  app.ui.state.self = { enabled: true, l2tp_enabled: false, kernel_running: true, can_bind: true,
    device: { id: 'phone', ip: '10.0.0.42', mac: '02:00:00:00:00:42', mode: 'proxy', node_id: 'l2tp-a' },
    nodes: [{ id: 'socks', type: 'socks5', name: 'SOCKS', enabled: true }], bound_node: connection({ enabled: true }) };
  app.ui.renderSelf();
  assert.match(app.el('#selfCurrentNode').textContent, /\[L2TP\].*Office/);
  assert.match(app.el('#selfServiceNotice').textContent, /管理员已关闭 L2TP/);
  assert.doesNotMatch(app.el('#selfNodeSelect').innerHTML, /Office/);
  app.document.hidden = false; app.ui.state.page = 'devices'; app.ui.syncL2TPPolling();
  assert.equal(app.intervals.size, 0);

  // A saved switch with failed cleanup must not masquerade as a confirmed stop.
  app = create(); app.ui.state.page = 'l2tp'; enabled = true;
  app.response = request => {
    if (request.method === 'PATCH') {
      enabled = request.body.enabled;
      return { httpStatus: 500, data: { error: 'L2TP 配置已保存，但应用失败：ifdown failed' } };
    }
    return { enabled, connections: [connection({ enabled: true, status: { state: enabled ? 'connected' : 'disabled', connected: enabled } })], dependencies: null };
  };
  await app.ui.refreshL2TP();
  moduleAction = app.ui.toggleL2TPModule(); app.ui.finishActionDialog(true); await moduleAction;
  assert.equal(app.ui.state.l2tpEnabled, false);
  assert.equal(app.ui.state.l2tpModuleError.enabled, false);
  assert.match(app.el('#l2tpLoadError').innerHTML, /配置已保存.*应用失败/);
  assert.match(app.el('#l2tpLoadError').innerHTML, /重试应用/);
  assert.equal(app.el('#l2tpLoadError').classList.contains('hidden'), false);
  assert.match(app.el('#l2tpModuleState').textContent, /应用未确认/);
  assert.doesNotMatch(app.el('#l2tpList').innerHTML, /L2TP 已关闭|l2tp-card-online/);
  assert.equal(app.el('#l2tpDisabledNotice').classList.contains('hidden'), true);
  assert.equal(app.intervals.size, 0);
  await app.ui.refreshL2TP(); await app.ui.refreshL2TP(true);
  assert.ok(app.ui.state.l2tpModuleError, 'Ordinary refreshes cannot confirm a failed apply');
  const afterReload = create(app.storage);
  afterReload.response = request => ({ enabled: request.body?.enabled ?? false, connections: [connection({ enabled: true, status: { state: 'disabled' } })], dependencies: null });
  await afterReload.ui.refreshL2TP();
  assert.ok(afterReload.ui.state.l2tpModuleError, 'The failure notice survives a browser reload');
  await afterReload.ui.retryL2TPModule();
  assert.equal(afterReload.requests.find(request => request.method === 'PATCH').body.enabled, false, 'Retry reapplies the same desired state instead of toggling it');
  assert.equal(afterReload.ui.state.l2tpModuleError, null);
  assert.equal(afterReload.storage.has('openpass.l2tpModuleError'), false);
  assert.equal(afterReload.el('#l2tpLoadError').classList.contains('hidden'), true);
  assert.equal(afterReload.el('#l2tpModuleState').textContent, 'L2TP 已关闭');

  // Runtime apply errors are authoritative across browsers, including successful retries elsewhere.
  app = create(); app.ui.state.page = 'l2tp';
  app.response = () => ({ enabled: false, apply_error: '已保存关闭设置，但隧道清理失败', connections: [connection({ enabled: true, status: { state: 'disabled' } })], dependencies: null });
  await app.ui.refreshL2TP();
  assert.equal(app.ui.state.l2tpModuleError.enabled, false);
  assert.match(app.el('#l2tpLoadError').innerHTML, /隧道清理失败/);
  assert.match(app.el('#l2tpModuleState').textContent, /应用未确认/);
  const sharedStorage = app.storage;
  app = create(sharedStorage);
  app.response = () => ({ enabled: false, apply_error: '', connections: [connection({ status: { state: 'disabled' } })], dependencies: null });
  await app.ui.refreshL2TP();
  assert.equal(app.ui.state.l2tpModuleError, null, 'An explicit empty apply_error clears an outdated local failure');
  assert.equal(sharedStorage.has('openpass.l2tpModuleError'), false);
  assert.equal(app.el('#l2tpModuleState').textContent, 'L2TP 已关闭');
  app.response = () => ({ enabled: true, apply_error: '路由规则应用失败', connections: [connection({ enabled: true, status: { state: 'connected', connected: true } })] });
  await app.ui.refreshL2TP();
  assert.equal(app.ui.state.l2tpModuleError.enabled, true);
  assert.doesNotMatch(app.el('#l2tpList').innerHTML, /l2tp-card-online/);
  app.response = request => request.method === 'PATCH' ? { enabled: request.body.enabled, apply_error: '' } : new Error('temporarily unreachable');
  await app.ui.retryL2TPModule();
  assert.equal(app.requests.find(request => request.method === 'PATCH').body.enabled, true);
  assert.equal(app.ui.state.l2tpModuleError, null, 'A confirmed successful apply clears the operation error even if the next read fails');
  assert.match(app.el('#l2tpLoadError').innerHTML, /读取连接状态失败/);

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
  app.ui.state.l2tpConnections = [connection({ enabled: true, username: ' person ' })];
  app.ui.openL2TPForm('l2tp-a');
  assert.equal(app.el('#l2tpPassword').value, '');
  assert.equal(app.el('#l2tpPassword').placeholder, '*****');
  app.el('#l2tpRemark').value = 'Updated note';
  await app.ui.saveL2TP({ preventDefault() {} });
  const edited = app.requests.find(request => request.method === 'PATCH');
  assert.equal(edited.body.remark, 'Updated note');
  assert.equal(edited.body.username, ' person ', 'Editing a remark must preserve significant account whitespace');
  assert.equal('password' in edited.body, false);
  assert.equal('enabled' in edited.body, false);
  app.ui.openL2TPForm(); app.el('#l2tpName').value = 'Blank account'; app.el('#l2tpAddress').value = 'vpn.example.test';
  app.el('#l2tpUsername').value = '   '; app.el('#l2tpPassword').value = 'secret';
  const beforeEmptyUsername = app.requests.length;
  await app.ui.saveL2TP({ preventDefault() {} });
  assert.equal(app.requests.length, beforeEmptyUsername); assert.match(app.el('#l2tpFormError').textContent, /用户名/);
  app.ui.closeL2TPForm();

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

  // Explicit copying retrieves complete credentials; no credentials are read on open.
  app = create(); app.ui.state.l2tpConnections = [connection({ username: 'private-user', password: 'private-secret' })];
  app.response = () => ({ address: 'vpn.example.test', username: 'private-user', password: 'private-secret:colon' });
  app.ui.openL2TPForm('l2tp-a');
  assert.equal(app.requests.length, 0);
  assert.equal(app.el('#l2tpPassword').value, '');
  app.ui.closeL2TPForm();
  await app.ui.runL2TPAction('copy', 'l2tp-a');
  assert.equal(app.requests[0].url, '/api/l2tp/l2tp-a/credentials');
  assert.equal(app.requests[0].method, 'POST');
  assert.equal(app.copied[0], 'vpn.example.test:private-user:private-secret:colon');
  assert.equal(app.ui.state.l2tpConnections[0].password, 'private-secret', 'Explicit credentials are never cached into the list');

  // Revealing an existing password is on demand and does not send it back on save.
  app = create(); app.ui.setupL2TPEvents(); app.ui.state.l2tpConnections = [connection()];
  app.response = request => request.url.endsWith('/credentials') ? { address: 'vpn.example.test', username: 'person', password: 'saved-secret' } : { connections: [connection()], enabled: true };
  app.ui.openL2TPForm('l2tp-a'); await app.ui.toggleL2TPPassword();
  assert.equal(app.el('#l2tpPassword').type, 'text'); assert.equal(app.el('#l2tpPassword').value, 'saved-secret');
  await app.ui.toggleL2TPPassword(); assert.equal(app.el('#l2tpPassword').type, 'password');
  await app.ui.toggleL2TPPassword(); assert.equal(app.requests.length, 1, 'Reuse the current form value, not another credential request');
  await app.ui.saveL2TP({ preventDefault() {} });
  assert.equal('password' in app.requests.find(request => request.method === 'PATCH').body, false);
  assert.equal(app.el('#l2tpPassword').value, ''); assert.equal(app.el('#l2tpUsername').value, '');
  app.ui.state.l2tpConnections = [connection()]; app.ui.openL2TPForm('l2tp-a'); await app.ui.toggleL2TPPassword();
  app.el('#l2tpPassword').value = 'replacement'; app.el('#l2tpPassword').handlers.input();
  await app.ui.saveL2TP({ preventDefault() {} });
  assert.equal(app.requests.filter(request => request.method === 'PATCH').at(-1).body.password, 'replacement');

  // Slow credential requests must not refill a closed form or replace new user input.
  app = create(); app.ui.setupL2TPEvents(); app.ui.state.l2tpConnections = [connection(), connection({ id: 'second' })];
  let resolveCredentials;
  app.response = () => new Promise(resolve => { resolveCredentials = resolve; });
  app.ui.openL2TPForm('l2tp-a'); let revealing = app.ui.toggleL2TPPassword();
  app.ui.closeL2TPForm(); app.ui.openL2TPForm('second');
  resolveCredentials({ password: 'first-secret' }); await revealing;
  assert.equal(app.el('#l2tpPassword').value, ''); assert.equal(app.el('#l2tpPassword').type, 'password');
  revealing = app.ui.toggleL2TPPassword(); app.el('#l2tpPassword').value = 'new-value'; app.el('#l2tpPassword').handlers.input();
  resolveCredentials({ password: 'old-secret' }); await revealing;
  assert.equal(app.el('#l2tpPassword').value, 'new-value');

  // Batch import preserves the supplied text, reports counts, and clears secrets on close.
  app = create(); app.ui.openL2TPImport();
  const bulk = 'vpn.example.com:user:password\nvpn2.example.com:user2:pass:with:colon';
  app.el('#l2tpImportText').value = bulk;
  app.response = request => request.url.endsWith('/import') ? { imported: 1, skipped: 1 } : { enabled: false, connections: [connection()], dependencies: null };
  await app.ui.importL2TP({ preventDefault() {} });
  const batchRequest = app.requests.find(request => request.method === 'POST');
  assert.equal(batchRequest.url, '/api/l2tp/import'); assert.equal(batchRequest.body.text, bulk);
  assert.equal(app.el('#l2tpImportText').value, '');
  assert.match(app.toasts().at(-1), /已添加 1 条 L2TP，跳过 1 条/);
  assert.equal(app.intervals.size, 0);
  app.ui.openL2TPImport(); app.el('#l2tpImportText').value = 'vpn.example.com:u:p\n'.repeat(501);
  const beforeBatch = app.requests.length;
  await app.ui.importL2TP({ preventDefault() {} });
  assert.equal(app.requests.length, beforeBatch); assert.match(app.el('#l2tpImportError').textContent, /最多.*500/);
  app.ui.closeL2TPImport(); assert.equal(app.el('#l2tpImportText').value, '');
  console.log('L2TP admin UI tests passed');
})().catch(error => { console.error(error); process.exitCode = 1; });
