#!/usr/bin/env node
'use strict';

// Exercise the real LuCI view with a small DOM/RPC harness. No router is contacted.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../../luci-app-openpass/htdocs/luci-static/resources/view/openpass/service.js'), 'utf8');

function element(tag, attrs = {}, children = []) {
  const node = {
    tag, attrs, children: (Array.isArray(children) ? children : [children]).flat(),
    style: {}, disabled: false, checked: attrs.checked != null,
    setAttribute(name, value) { this.attrs[name] = value; },
    removeAttribute(name) { delete this.attrs[name]; },
    get textContent() { return this.text === undefined ? this.children.map(c => typeof c === 'object' ? c.textContent : c).join('') : this.text; },
    set textContent(value) { this.text = value; }
  };
  return node;
}
function descendants(root) {
  return [root, ...(root.children || []).filter(c => c && typeof c === 'object').flatMap(descendants)];
}
function button(root, label) {
  const result = descendants(root).find(n => n.tag === 'button' && n.textContent === label);
  assert.ok(result, 'Missing button: ' + label);
  return result;
}
function create(overrides = {}, status = { running: true, enabled: true, stopped: false, version: '0.1.12' }, maintenance) {
  const calls = [], notifications = [];
  const responses = Object.assign({ status, maintenance_status: { state: 'idle' } }, overrides);
  let modal = null, poller = null, removed = false;
  const view = new Function('view', 'rpc', 'poll', 'ui', 'L', 'E', '_', 'window', source)(
    { extend: config => config },
    { declare: spec => (...args) => {
      calls.push({ method: spec.method, args });
      return Promise.resolve().then(() => {
        const response = responses[spec.method];
        return typeof response === 'function' ? response(...args) : response;
      });
    } },
    { add: callback => { poller = callback; }, remove: callback => { assert.equal(callback, poller); removed = true; } },
    { showModal: (title, content) => { modal = element('modal', {}, content); }, hideModal: () => { modal = null; },
      addNotification: (_, content, severity) => notifications.push({ text: content.textContent, severity }) },
    { env: {}, url: value => '/cgi-bin/luci/' + value }, element, value => value,
    { location: { href: 'http://10.0.0.1/cgi-bin/luci/admin/services/openpass' } }
  );
  const root = view.render({ status, maintenance });
  return { root, calls, notifications, responses, view, get modal() { return modal; }, get removed() { return removed; },
    click: label => button(root, label).attrs.click(), modalClick: label => button(modal, label).attrs.click(),
    poll: () => poller() };
}

(async () => {
  let app = create();
  assert.match(app.root.textContent, /当前版本：v0\.1\.12/);
  const actions = app.root.children.at(-1).children;
  assert.deepEqual(actions.map(a => a.textContent), ['打开管理页面', '打开设备自助页', '检查更新', '卸载 OpenPass']);
  app.click('卸载 OpenPass');
  assert.equal(descendants(app.modal).find(n => n.tag === 'input').checked, true);
  app.modalClick('取消');
  assert.equal(app.calls.length, 0, 'Cancel must not invoke uninstall RPC');

  app = create({}, null);
  assert.equal(button(app.root, '检查更新').disabled, true);
  assert.equal(button(app.root, '卸载 OpenPass').disabled, true);
  app.click('卸载 OpenPass');
  assert.equal(app.modal, null);

  app = create({}, { running: false, enabled: false, stopped: true, version: '0.1.12' });
  assert.equal(button(app.root, '检查更新').disabled, false, 'Stopped service can check updates');
  assert.equal(button(app.root, '卸载 OpenPass').disabled, false, 'Stopped service can uninstall');

  app = create({ uninstall: { error: 'cleanup failed' } });
  app.click('卸载 OpenPass');
  await app.modalClick('确认卸载');
  assert.deepEqual(app.calls[0], { method: 'uninstall', args: [true, true] });
  assert.equal(app.notifications.length, 1);
  assert.equal(app.notifications[0].severity, 'error');
  assert.doesNotMatch(app.root.textContent, /OpenPass 已卸载/);
  assert.equal(app.removed, false);

  app = create({ uninstall: { uninstalled: true, config_kept: false } });
  app.click('卸载 OpenPass');
  descendants(app.modal).find(n => n.tag === 'input').checked = false;
  await app.modalClick('确认卸载');
  assert.deepEqual(app.calls[0].args, [false, true]);
  assert.equal(app.removed, true, 'Successful uninstall removes the polling callback');
  const callCount = app.calls.length;
  await app.poll();
  assert.equal(app.calls.length, callCount, 'Never request the removed RPC after successful uninstall');
  assert.match(app.root.textContent, /OpenPass 已卸载/);

  app = create({ check_update: { error: 'GitHub unavailable' } });
  await app.click('检查更新');
  assert.equal(app.modal, null);
  assert.equal(app.notifications[0].severity, 'error');

  const release = { current_version: '0.1.12', latest_version: '0.1.13', update_available: true };
  app = create({ check_update: release, update: { error: 'download rejected' } });
  await app.click('检查更新');
  await app.modalClick('立即更新');
  assert.deepEqual(app.calls.find(c => c.method === 'update').args, ['v0.1.13']);
  assert.ok(app.notifications.every(n => n.severity === 'error'), 'Rejected update cannot report success');

  app = create({ check_update: release, update: { accepted: true }, maintenance_status: () => { throw new Error('RPC restarting'); } });
  await app.click('检查更新');
  await app.modalClick('立即更新');
  assert.equal(app.notifications.length, 0, 'Transient RPC failure is not a completed update');
  assert.equal(button(app.root, '卸载 OpenPass').disabled, true);
  app.responses.maintenance_status = { state: 'success', action: 'update', version: '0.1.12' };
  await app.poll();
  assert.equal(app.notifications.length, 0, 'A previous job success cannot complete the new update');
  app.responses.maintenance_status = { state: 'success', action: 'update', version: '0.1.13' };
  app.responses.status = { running: true, enabled: true, stopped: false, version: '0.1.13' };
  await app.poll();
  assert.equal(app.notifications.at(-1).severity, 'info');
  assert.match(app.notifications.at(-1).text, /已更新到 v0\.1\.13/);
  assert.match(app.root.textContent, /当前版本：v0\.1\.13/);

  app = create({}, undefined, { state: 'running', action: 'update', version: 'v0.1.13' });
  assert.equal(button(app.root, '卸载 OpenPass').disabled, true, 'Reload resumes the existing update');
  app.responses.maintenance_status = { state: 'error', action: 'update', message: 'install failed' };
  await app.poll();
  assert.equal(app.notifications.at(-1).severity, 'error');
  console.log('LuCI maintenance UI tests passed');
})().catch(error => { console.error(error); process.exitCode = 1; });
