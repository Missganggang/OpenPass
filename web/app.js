/* OpenPass admin UI: plain browser JavaScript, no runtime dependencies. */
(function () {
  'use strict';
  const API = '/api';
  const $ = (s, root = document) => root.querySelector(s);
  const $$ = (s, root = document) => Array.from(root.querySelectorAll(s));
  const state = {
    page: 'dashboard', deviceTab: 'online', nodeFilter: 'all',
    devices: [], allDevices: [], nodes: [], subscriptions: [], nodeSubscriptionFilter: 'all', settings: {}, dns: [], status: {},
    standalone: /^\/(choose|self)\/?$/.test(location.pathname),
    self: null, selfMode: null, selfLoading: false, pendingProxy: new Set(), deletingNodes: new Set(),
    l2tpConnections: [], l2tpDependencies: null, l2tpLoaded: false, l2tpError: '', l2tpUpdatedAt: null,
    l2tpPending: new Set(), l2tpRefreshPromise: null, l2tpTimer: null, l2tpEditing: null, l2tpSaving: false,
    demo: false
  };

  const demoDns = [{ id: 'cloudflare', name: 'Cloudflare', url: 'https://cloudflare-dns.com/dns-query' }, { id: 'aliyun', name: '阿里 DoH · 223.5.5.5', url: 'https://223.5.5.5/dns-query' }, { id: 'aliyun-secondary', name: '阿里 DoH · 223.6.6.6', url: 'https://223.6.6.6/dns-query' }, { id: 'tencent', name: '腾讯 DoH · 120.53.53.53', url: 'https://doh.pub/dns-query' }];

  let pendingActionDialog = null;

  function finishActionDialog(value) {
    if (!pendingActionDialog) return;
    const current = pendingActionDialog;
    pendingActionDialog = null;
    $('#actionDialogBackdrop').classList.add('hidden');
    $('.app-shell').inert = false;
    if (current.previousFocus?.isConnected) current.previousFocus.focus();
    current.resolve(value);
  }
  function showActionDialog({ title, message, label, value = '', submitText = '保存', danger = false, maxLength = 200 }) {
    if (pendingActionDialog) finishActionDialog(null);
    const previousFocus = document.activeElement;
    $('#actionDialogTitle').textContent = title;
    $('#actionDialogMessage').textContent = message || '';
    $('#actionDialogLabel').textContent = label || '';
    $('#actionDialogInput').value = value;
    $('#actionDialogField').classList.toggle('hidden', !label);
    $('#actionDialogInput').disabled = !label;
    $('#actionDialogSubmit').textContent = submitText;
    $('#actionDialogSubmit').classList.toggle('btn-danger', danger);
    $('#actionDialogError').textContent = '';
    $('#actionDialogError').classList.add('hidden');
    $('#actionDialogBackdrop').classList.remove('hidden');
    $('.app-shell').inert = true;
    return new Promise(resolve => {
      pendingActionDialog = { resolve, previousFocus, hasInput: !!label, maxLength };
      (label ? $('#actionDialogInput') : $('#actionDialogCancel')).focus();
      if (label) $('#actionDialogInput').select();
    });
  }
  function setupActionDialogEvents() {
    $('#actionDialogCancel').addEventListener('click', () => finishActionDialog(null));
    $('#actionDialogClose').addEventListener('click', () => finishActionDialog(null));
    $('#actionDialogBackdrop').addEventListener('click', event => { if (event.target.id === 'actionDialogBackdrop') finishActionDialog(null); });
    $('#actionDialogForm').addEventListener('submit', event => {
      event.preventDefault();
      if (!pendingActionDialog) return;
      const value = $('#actionDialogInput').value.trim();
      if (pendingActionDialog.hasInput && Array.from(value).length > pendingActionDialog.maxLength) {
        $('#actionDialogError').textContent = `备注不能超过 ${pendingActionDialog.maxLength} 字`;
        $('#actionDialogError').classList.remove('hidden');
        $('#actionDialogInput').focus();
        return;
      }
      finishActionDialog(pendingActionDialog.hasInput ? value : true);
    });
    document.addEventListener('keydown', event => {
      if (!pendingActionDialog) return;
      if (event.key === 'Escape') { event.preventDefault(); finishActionDialog(null); }
      if (event.key === 'Tab') {
        const controls = [$('#actionDialogClose'), ...(pendingActionDialog.hasInput ? [$('#actionDialogInput')] : []), $('#actionDialogCancel'), $('#actionDialogSubmit')];
        const first = controls[0]; const last = controls[controls.length - 1];
        if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
      }
    });
  }

  async function api(path, options = {}) {
    const opts = { credentials: 'include', headers: { 'Content-Type': 'application/json', ...(options.headers || {}) }, ...options };
    if (opts.body && typeof opts.body !== 'string') opts.body = JSON.stringify(opts.body);
    const response = await fetch(API + path, opts);
    const text = await response.text();
    let data = {};
    try { data = text ? JSON.parse(text) : {}; } catch (_) { /* Error responses may be plain text. */ }
    if (!response.ok) { const error = new Error(data?.error || data?.message || text.trim() || `HTTP ${response.status}`); error.status = response.status; error.data = data; throw error; }
    return data;
  }

  function toast(message, type = 'success') {
    const stack = $('#toastStack'); if (!stack) return;
    const item = document.createElement('div'); item.className = `toast ${type}`; item.textContent = message; stack.appendChild(item);
    setTimeout(() => item.remove(), 3300);
  }
  function esc(value) { return String(value == null ? '' : value).replace(/[&<>'"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[c])); }
  function responseList(payload, key) { const list = Array.isArray(payload) ? payload : payload?.[key]; return Array.isArray(list) ? list.filter(item => item && typeof item === 'object') : []; }
  function findNode(id) { return state.nodes.find(n => String(n.id) === String(id)); }
  function nodeLabel(node) { return [isL2TP(node) ? '[L2TP]' : '', node?.name || node?.address || node?.type || '代理节点', node?.remark].filter(Boolean).join(' · '); }
  function isL2TP(node) { return String(node?.type || '').toLowerCase() === 'l2tp'; }
  function regularNodes() { return state.nodes.filter(node => !isL2TP(node)); }
  function nodeSubscriptionID(node) {
    if (!node) return '';
    if (node.subscription_id != null) return String(node.subscription_id);
    if (node.subscription && typeof node.subscription === 'object') return String(node.subscription.id || '');
    return '';
  }
  function subscriptionName(id) {
    const subscription = state.subscriptions.find(item => String(item.id) === String(id));
    return subscription?.name || subscription?.remark || '订阅节点';
  }
  function nodeName(id) { return findNode(id)?.name || (id ? '节点已删除' : '未绑定节点'); }
  function dnsName(id) { return state.dns.find(d => String(d.id) === String(id))?.name || id || '默认 DoH'; }
  function timeText(value) { if (!value) return '—'; if (typeof value === 'string') return value; try { return new Date(value).toLocaleString('zh-CN', { hour: '2-digit', minute: '2-digit' }); } catch (_) { return '—'; } }
  function selfURL() { return `${location.origin}/choose`; }

  async function bootstrap() {
    if (state.standalone) return loadStandalone();
    let status;
    try { status = await api('/status'); state.status = status || {}; } catch (error) {
      state.status = { enabled: false, kernel: '后端连接失败' }; renderAll(); toast(`无法连接后端：${error.message}`, 'error'); return;
    }
    await loadData();
  }

  async function loadData(throwOnError = false) {
    try {
      const [devices, allDevices, nodes, settings, dns, status, subscriptions] = await Promise.all([api('/devices'), api('/devices?hidden=all').catch(() => null), api('/nodes'), api('/settings'), api('/dns').catch(() => demoDns), api('/status'), api('/subscriptions').catch(() => [])]);
      state.devices = responseList(devices, 'devices'); const everyDevice = allDevices ? responseList(allDevices, 'devices') : state.devices;
      state.nodes = responseList(nodes, 'nodes').map(n => ({ ...n, test_results: findNode(n.id)?.test_results })); state.settings = settings || {}; state.dns = Array.isArray(dns) ? responseList(dns) : demoDns;
      state.subscriptions = responseList(subscriptions, 'subscriptions');
      if (state.nodeSubscriptionFilter !== 'all' && !state.subscriptions.some(item => String(item.id) === String(state.nodeSubscriptionFilter))) state.nodeSubscriptionFilter = 'all';
      state.allDevices = everyDevice;
      state.status = status || {};
      if (state.settings.enabled != null) state.status.enabled = state.settings.enabled;
    } catch (error) {
      if (throwOnError) throw error;
      toast(`数据加载失败：${error.message}`, 'error');
    }
    renderAll();
  }


  async function loadStandalone() {
    document.body.classList.add('standalone-self');
    $$('.page').forEach(page => page.classList.toggle('hidden', page.dataset.pageView !== 'self-service'));
    return loadSelf();
  }
  async function loadSelf() {
    state.selfLoading = true;
    $('#selfConfirmBtn').disabled = true;
    $('#selfRefreshBtn').disabled = true;
    $('#selfError').classList.add('hidden');
    try {
      state.self = await api('/self');
      renderSelf();
    } catch (error) {
      state.self = null;
      renderSelf();
      $('#selfError').textContent = `无法识别当前设备：${error.message}`;
      $('#selfError').classList.remove('hidden');
    } finally {
      state.selfLoading = false;
      $('#selfRefreshBtn').disabled = false;
    }
  }
  function renderSelf() {
    const data = state.self || {};
    const device = data.device;
    const nodes = responseList(data, 'nodes').filter(n => n.enabled !== false);
    const identified = !!(device?.ip && device?.mac) && data.can_bind !== false;
    $('#selfPreviewDevice').textContent = device?.hostname || '当前访问设备';
    $('#selfDeviceIP').textContent = device?.ip || data.ip || '未识别';
    $('#selfDeviceMAC').textContent = device?.mac || '未识别';
    $('#selfDeviceRemark').textContent = device?.remark || '暂无备注';
    $('#selfCurrentMode').textContent = device ? ({ proxy: '代理节点', direct: '本地直连', blocked: '禁止网络' }[device.mode] || '未设置') : '未识别';
    const boundNode = responseList(data, 'nodes').find(n => n.id === device?.node_id);
    $('#selfCurrentNode').textContent = device?.mode === 'proxy' && device.node_id ? (boundNode ? nodeLabel(boundNode) : '节点已删除或停用') : '未绑定';
    const enabled = data.enabled ?? data.protection_enabled ?? state.settings.enabled;
    const inactive = enabled === false || data.kernel_running === false;
    $('#selfServiceNotice').textContent = enabled === false ? '全局保护当前关闭，现有代理绑定未生效。选择代理节点并确认后将自动开启并应用。' : data.kernel_running === false ? '代理内核当前未运行，现有代理绑定未生效。请重新确认连接方式；如仍无法连接，请联系管理员。' : enabled === true ? '选择代理节点后立即应用；本地直连默认使用阿里 DoH（223.5.5.5）。' : '请先确认设备信息，再选择网络方式。';
    $('#selfServiceNotice').classList.toggle('notice-warning', inactive);
    if (device?.mode === 'proxy' && inactive) $('#selfCurrentMode').textContent = '代理节点（未生效）';
    $('#selfNodeSelect').innerHTML = '<option value="">请选择代理节点</option>' + nodes.map(n => `<option value="${esc(n.id)}">${esc(nodeLabel(n))}</option>`).join('');
    $('#selfNodeSelect').value = device?.mode === 'proxy' ? (device.node_id || '') : '';
    $('#selfNoNodes').classList.toggle('hidden', nodes.length > 0);
    $$('.self-choice').forEach(choice => { choice.disabled = !identified || (choice.dataset.selfMode === 'proxy' && nodes.length === 0); });
    setSelfMode(device?.mode === 'proxy' ? 'proxy' : device?.mode === 'direct' ? 'direct' : null);
    $('#selfConfirmBtn').disabled = !identified;
    if (!identified && state.self) {
      $('#selfError').textContent = data.identification_error || data.message || '尚未识别到此设备的 MAC 地址。请连接本路由器局域网后刷新；使用 AP 时请关闭 AP 的二次 NAT。';
      $('#selfError').classList.remove('hidden');
    }
    if ($('.self-url span')) $('.self-url span').textContent = selfURL();
    $$('.self-instructions-url').forEach(el => { el.textContent = selfURL(); });
  }
  function setSelfMode(mode) {
    state.selfMode = mode;
    $$('.self-choice').forEach(choice => {
      const selected = choice.dataset.selfMode === mode;
      choice.classList.toggle('selected', selected);
      choice.setAttribute('aria-pressed', String(selected));
    });
    $('#selfNodeField').classList.toggle('hidden', mode !== 'proxy');
  }

  function navigate(page) {
    page = page || 'dashboard'; state.page = page; location.hash = page;
    $$('.page').forEach(el => el.classList.toggle('hidden', el.dataset.pageView !== page));
    $$('.nav-item').forEach(el => el.classList.toggle('active', el.dataset.page === page));
    const title = { dashboard: '仪表盘', devices: '设备管理', nodes: '节点列表', l2tp: 'L2TP 连接', dns: 'DNS 防泄露', settings: '设置', 'self-service': '设备自助页' }[page] || '仪表盘';
    $('#pageTitle').textContent = title;
    $('#sidebar')?.classList.remove('open');
    if (page === 'devices') renderDevices(); if (page === 'nodes') { renderSubscriptions(); renderNodes(); }
    syncL2TPPolling();
    if (page === 'self-service' && !state.standalone) loadSelf();
  }

  function renderAll() { renderStatus(); renderDashboard(); renderDevices(); renderSubscriptions(); renderNodes(); renderSettings(); renderDns(); navigate(state.page); }
  function renderStatus() {
    $('#appVersion').textContent = `OpenPass v${String(state.status.version || '0.1.13').replace(/^v/, '')}`;
    const online = state.allDevices.filter(d => d.online && !d.hidden).length; const proxied = state.allDevices.filter(d => d.mode === 'proxy' && !d.hidden).length; const healthy = state.nodes.filter(n => nodeURLResult(n) === true && n.enabled !== false).length;
    $('#metricOnline').textContent = online; $('#metricProxied').textContent = proxied; $('#metricNodes').textContent = healthy; $('#onlineBadge').textContent = online; $('#nodesBadge').textContent = regularNodes().length; $('#onlineCount').textContent = online; $('#offlineCount').textContent = state.allDevices.filter(d => !d.online && !d.hidden).length; $('#hiddenCount').textContent = state.allDevices.filter(d => d.hidden).length; $('#hiddenTabCount').textContent = state.allDevices.filter(d => d.hidden).length;
    if ($('#l2tpBadge')) $('#l2tpBadge').textContent = state.nodes.filter(isL2TP).length;
    $('#metricDns').textContent = state.settings.default_dns ? dnsName(state.settings.default_dns) : 'DoH 安全'; $('#metricDnsSub').textContent = state.settings.force_doh === false ? '加密解析未强制' : 'DoH 加密解析'; $('#kernelVersion').textContent = state.status.kernel || state.status.version || 'sing-box 运行中'; $('#routerAddress').textContent = state.status.router || 'OpenWrt · 10.0.0.1';
    const protect = $('#quickProtect'); if (protect) { const on = !!(state.settings.enabled ?? state.status.enabled); protect.innerHTML = on ? '✓ 全局保护已开启' : '◉ 开启全局保护'; protect.classList.toggle('btn-ghost', on); protect.classList.toggle('btn-primary', !on); }
    const runtimeNotice = $('#deviceRuntimeNotice');
    if (runtimeNotice) {
      const enabled = state.settings.enabled ?? state.status.enabled;
      runtimeNotice.textContent = enabled === false ? '全局保护已关闭，代理绑定当前未生效。选择代理节点后将自动开启并应用。' : state.status.kernel_running === false ? '代理内核当前未运行，代理绑定未生效。请重新绑定节点或在设置中重载内核。' : '';
      runtimeNotice.classList.toggle('hidden', !runtimeNotice.textContent);
    }
    if (state.status.kernel_running === false) $('#kernelVersion').textContent = 'sing-box 未运行';
    $('.kernel-state i')?.classList.toggle('stopped', state.status.kernel_running === false);
  }
  function renderDashboard() {
    const visible = state.allDevices.filter(d => d.online && !d.hidden).slice(0, 4); $('#dashboardDevices').innerHTML = visible.length ? visible.map(d => `<div class="mini-device"><span class="device-avatar">◉</span><span class="identity"><b>${esc(d.hostname || '未命名设备')}</b><small>${esc(d.ip)} · ${esc(d.mac)}</small></span><span class="device-mode">${d.mode === 'proxy' ? esc(nodeName(d.node_id)) : d.mode === 'direct' ? '直连网络' : '已阻断'}</span></div>`).join('') : '<div class="loading-row">暂无在线设备</div>';
    const nodes = regularNodes().slice(0, 4); $('#dashboardNodes').innerHTML = nodes.length ? nodes.map(n => { const latency = n.test_results?.url?.ok ? n.test_results.url.latency_ms : null; return `<div class="node-health"><span class="health-dot ${nodeURLResult(n) !== true ? 'off' : ''}"></span><span class="identity"><b>${esc(n.name || n.address)}</b><small>${esc((n.type || '').toUpperCase())} · ${esc(n.address || '')}</small></span><span class="latency">${latency ? `${latency} ms` : '待测试'}</span></div>`; }).join('') : '<div class="loading-row">暂无节点</div>';
  }
  function deviceModeSelect(d) {
    const pending = state.pendingProxy.has(String(d.id));
    const mode = pending ? 'proxy' : d.mode;
    const inactive = mode === 'proxy' && ((state.settings.enabled ?? state.status.enabled) === false || state.status.kernel_running === false);
    return `<select class="mode-select ${esc(mode || '')}" data-action="mode" data-id="${esc(d.id)}" aria-label="设备连接方式"><option value="proxy" ${mode === 'proxy' ? 'selected' : ''}>代理节点</option><option value="direct" ${mode === 'direct' ? 'selected' : ''}>直连网络</option><option value="blocked" ${mode === 'blocked' ? 'selected' : ''}>禁止网络</option></select>${mode === 'proxy' ? `<select class="mode-select proxy node-select" data-action="node" data-id="${esc(d.id)}" aria-label="绑定代理节点"><option value="">选择节点</option>${state.nodes.filter(n => n.enabled !== false).map(n => `<option value="${esc(n.id)}" ${!pending && String(d.node_id) === String(n.id) ? 'selected' : ''}>${esc(nodeLabel(n))}</option>`).join('')}</select>` : ''}${pending ? '<small class="device-policy-note">选择节点后立即绑定</small>' : inactive ? '<small class="device-policy-note warning">代理当前未生效</small>' : ''}`;
  }
  function dnsSelect(d) { const all = state.dns.length ? state.dns.slice() : demoDns.slice(); if (state.settings.custom_dns && !all.some(x => x.id === 'custom')) all.push({ id: 'custom', name: '自定义 DoH' }); const selected = d.dns || (d.mode === 'direct' ? 'aliyun' : 'cloudflare'); return `<select class="mode-select" data-action="dns" data-id="${esc(d.id)}" aria-label="设备 DNS">${all.map(x => `<option value="${esc(x.id)}" ${String(selected) === String(x.id) ? 'selected' : ''}>${esc(x.name)}</option>`).join('')}</select>`; }
  function renderDevices() {
    const query = ($('#deviceSearch')?.value || '').toLowerCase(); const source = state.deviceTab === 'hidden' ? state.allDevices.filter(d => d.hidden) : state.allDevices.filter(d => !d.hidden && (state.deviceTab === 'online' ? d.online : !d.online)); const rows = source.filter(d => [d.hostname, d.remark, d.ip, d.mac].some(v => String(v || '').toLowerCase().includes(query)));
    const body = $('#devicesTable'); if (!body) return; if (!rows.length) { body.innerHTML = `<tr><td colspan="6" class="empty-state">${state.deviceTab === 'hidden' ? '暂无隐藏设备' : '暂无符合条件的设备'}</td></tr>`; return; }
    body.innerHTML = rows.map(d => `<tr><td><div class="device-cell"><span class="device-avatar">${d.hidden ? '⌁' : '◉'}</span><span class="device-name">${esc(d.hostname || '未命名设备')}${d.remark ? `<small class="device-remark">备注：${esc(d.remark)}</small>` : ''}<small><span class="status-label ${d.online ? '' : 'off'}"><i></i>${d.online ? '在线' : '离线'}</span></small></span></div></td><td class="mono">${esc(d.ip || '—')}</td><td class="mono">${esc(d.mac || '—')}</td><td>${d.hidden ? '<span class="muted">已隐藏</span>' : deviceModeSelect(d)}</td><td>${dnsSelect(d)}</td><td class="align-right"><span class="row-actions"><button class="row-action text-action" data-action="remark-device" data-id="${esc(d.id)}" title="编辑设备备注">备注</button><button class="row-action" data-action="hide" data-id="${esc(d.id)}" title="${d.hidden ? '取消隐藏' : '隐藏设备'}">${d.hidden ? '⊙' : '◌'}</button><button class="row-action danger" data-action="block" data-id="${esc(d.id)}" title="阻断设备">⊘</button><button class="row-action text-action danger" data-action="release-device" data-id="${esc(d.id)}" title="释放设备记录和绑定">释放</button></span></td></tr>`).join('');
  }
  const testLabels = { ping: 'Ping', tcp: 'TCPing', url: 'URL' };
  function nodeURLResult(node) { return node.test_results?.url?.ok ?? node.url_reachable; }
  function nodeTestSummary(node) {
    return ['ping', 'tcp', 'url'].map(type => {
      const result = node.test_results?.[type];
      if (!result) return `<div class="test-detail muted">${testLabels[type]} · 未检测</div>`;
      const details = [result.error, result.note].filter(Boolean).join(' · ');
      return `<div class="test-detail ${result.ok ? 'test-pass' : 'test-fail'}"><b>${testLabels[type]} · ${result.ok ? '通过' : '失败'}</b>${result.ok ? ` · ${esc(result.latency_ms ?? '—')} ms` : ''}${result.status ? ` · HTTP ${esc(result.status)}` : ''}${details ? `<small>${esc(details)}</small>` : ''}</div>`;
    }).join('');
  }
  function renderSubscriptions() {
    const body = $('#subscriptionsList');
    if (!body) return;
    const countLabel = $('#subscriptionCount');
    if (countLabel) countLabel.textContent = `${state.subscriptions.length} 个订阅`;
    if (!state.subscriptions.length) {
      body.innerHTML = '<div class="subscription-empty">暂无订阅地址。点击“导入节点”，选择“订阅地址”即可添加。</div>';
      return;
    }
    body.innerHTML = state.subscriptions.map(subscription => {
      const id = String(subscription.id || '');
      const count = state.nodes.filter(node => nodeSubscriptionID(node) === id || (node.subscription_name && String(node.subscription_name) === String(subscription.name))).length || Number(subscription.node_count || 0);
      const selected = state.nodeSubscriptionFilter === id;
      const sync = subscription.last_sync_at ? `上次更新 ${timeText(subscription.last_sync_at)}` : subscription.last_error ? `更新失败：${subscription.last_error}` : '尚未同步';
      return `<article class="subscription-card ${selected ? 'selected' : ''}" data-subscription-id="${esc(id)}"><div class="subscription-card-main"><span class="subscription-icon">↻</span><div><b>${esc(subscription.name || '未命名订阅')}</b>${subscription.remark ? `<small>备注：${esc(subscription.remark)}</small>` : ''}<small class="subscription-meta">${esc(sync)} · ${count} 个节点</small></div></div><div class="subscription-card-actions"><button class="row-action text-action" data-action="select-subscription" data-id="${esc(id)}">${selected ? '正在查看' : '查看节点'}</button><button class="row-action" data-action="refresh-subscription" data-id="${esc(id)}" title="立即刷新订阅">↻</button><button class="row-action" data-action="edit-subscription" data-id="${esc(id)}" title="编辑订阅备注">备注</button><button class="row-action danger" data-action="delete-subscription" data-id="${esc(id)}" title="删除订阅">×</button></div></article>`;
    }).join('');
  }
  function renderNodes() {
    ensureNodeTestControls();
    const query = ($('#nodeSearch')?.value || '').toLowerCase();
    const ordinary = regularNodes();
    let rows = ordinary.filter(n => [n.name, n.remark, n.type, n.address, n.subscription_name, nodeSubscriptionID(n) ? subscriptionName(nodeSubscriptionID(n)) : ''].some(value => String(value || '').toLowerCase().includes(query)));
    if (state.nodeSubscriptionFilter !== 'all') rows = rows.filter(node => nodeSubscriptionID(node) === String(state.nodeSubscriptionFilter) || String(node.subscription_name || '') === subscriptionName(state.nodeSubscriptionFilter));
    if (state.nodeFilter === 'healthy') rows = rows.filter(n => nodeURLResult(n) === true);
    if (state.nodeFilter === 'unhealthy') rows = rows.filter(n => nodeURLResult(n) === false);
    $('#nodeAvailable').textContent = ordinary.filter(n => nodeURLResult(n) === true).length;
    $('#nodeChecking').textContent = ordinary.filter(n => n.testing).length;
    $('#nodeUnavailable').textContent = ordinary.filter(n => nodeURLResult(n) === false).length;
    const body = $('#nodesTable');
    if (!body) return;
    const activeSubscription = state.nodeSubscriptionFilter === 'all' ? '' : subscriptionName(state.nodeSubscriptionFilter);
    const clear = $('#nodeSubscriptionClear');
    if (clear) { clear.classList.toggle('hidden', !activeSubscription); clear.textContent = activeSubscription ? `仅显示：${activeSubscription} ×` : ''; }
    if (!rows.length) { body.innerHTML = `<tr><td colspan="6" class="empty-state">${activeSubscription ? `“${esc(activeSubscription)}”暂无节点` : '暂无符合条件的节点'}</td></tr>`; return; }
    body.innerHTML = rows.map(node => {
      const urlOK = nodeURLResult(node);
      const result = node.test_results?.url;
      const label = node.testing ? `${testLabels[node.testing]} 检测中` : urlOK === true ? 'URL 通过' : urlOK === false ? 'URL 失败' : '待 URL 测试';
      const statusClass = urlOK === false ? 'bad' : urlOK === true ? '' : 'off';
      const actions = ['ping', 'tcp', 'url'].map(type => `<button class="row-action test-action" data-action="test-node" data-test-type="${type}" data-id="${esc(node.id)}" title="${testLabels[type]} 测试" ${node.testing ? 'disabled' : ''}>${testLabels[type]}</button>`).join('');
      const subLabel = nodeSubscriptionID(node) ? subscriptionName(nodeSubscriptionID(node)) : '';
      return `<tr><td><div class="device-cell"><span class="device-avatar">◈</span><span class="device-name">${esc(node.name || node.address)}${node.remark ? `<small class="node-remark">备注：${esc(node.remark)}</small>` : ''}${subLabel ? `<small class="node-remark subscription-node-label">订阅：${esc(subLabel)}</small>` : ''}<small>${esc(node.address || '')}:${esc(node.port || '')}</small></span></div></td><td><span class="protocol-badge ${esc(node.type || '')}">${esc(node.type || 'unknown')}</span></td><td><span class="latency">${result?.ok ? `${esc(result.latency_ms)} ms` : '—'}</span><small class="test-caption">代理 URL 延迟</small></td><td><span class="status-label ${statusClass}"><i></i>${label}</span><div class="node-test-results">${nodeTestSummary(node)}</div></td><td class="muted">${esc(node.last_test ? timeText(node.last_test) : '—')}</td><td class="align-right"><span class="row-actions"><button class="row-action" data-action="remark-node" data-id="${esc(node.id)}" title="编辑备注">备注</button><button class="row-action node-share" data-action="copy-node" data-id="${esc(node.id)}" title="复制节点链接">复制</button><button class="row-action node-share" data-action="export-node" data-id="${esc(node.id)}" title="导出节点链接">导出</button>${actions}<button class="row-action danger" data-action="delete-node" data-id="${esc(node.id)}" title="删除节点" ${state.deletingNodes.has(String(node.id)) ? 'disabled' : ''}>×</button></span></td></tr>`;
    }).join('');
  }
  function l2tpOnline(connection) { return connection?.status?.connected === true || ['connected', 'online'].includes(connection?.status?.state); }
  function l2tpStatus(connection) {
    if (state.l2tpError) return { key: 'unknown', label: '状态待确认' };
    if (l2tpOnline(connection)) return { key: 'online', label: '在线' };
    const status = connection?.status || {};
    if (status.state === 'connecting') return { key: 'connecting', label: '连接中' };
    if (status.state === 'error') return { key: 'error', label: '连接异常' };
    if (status.state === 'unavailable') return { key: 'error', label: '暂不可用' };
    return { key: 'offline', label: '已断开' };
  }
  function l2tpDuration(seconds) {
    if (!Number.isFinite(Number(seconds)) || Number(seconds) <= 0) return '—';
    seconds = Math.floor(Number(seconds));
    const days = Math.floor(seconds / 86400), hours = Math.floor(seconds % 86400 / 3600), minutes = Math.floor(seconds % 3600 / 60);
    return [days ? `${days} 天` : '', hours ? `${hours} 小时` : '', minutes ? `${minutes} 分钟` : '', !days && !hours && !minutes ? `${seconds} 秒` : ''].filter(Boolean).join(' ');
  }
  function l2tpByID(id) { return state.l2tpConnections.find(connection => String(connection.id) === String(id)); }
  function l2tpBoundCount(connection) {
    return Number.isInteger(connection.bound_devices) ? connection.bound_devices : state.allDevices.filter(device => String(device.node_id || '') === String(connection.id)).length;
  }
  function renderL2TP() {
    const list = $('#l2tpList');
    if (!list) return;
    const connections = state.l2tpConnections;
    $('#l2tpOnlineCount').textContent = state.l2tpError ? '—' : connections.filter(l2tpOnline).length;
    $('#l2tpTotalCount').textContent = `${connections.length} 条连接`;
    $('#l2tpBadge').textContent = connections.length;
    $('#l2tpRefreshTime').textContent = state.l2tpRefreshPromise ? '正在刷新连接状态…' : state.l2tpError ? '刷新失败，请重试；以下数据可能已过期' : state.l2tpUpdatedAt ? `更新于 ${state.l2tpUpdatedAt.toLocaleTimeString('zh-CN')} · 每 5 秒自动刷新` : '此页面每 5 秒刷新连接状态';
    const error = $('#l2tpLoadError'); error.textContent = state.l2tpError ? `读取连接状态失败：${state.l2tpError}` : ''; error.classList.toggle('hidden', !state.l2tpError);
    const dependencies = state.l2tpDependencies;
    const dependencyNotice = $('#l2tpDependencies');
    const missing = dependencies && dependencies.available === false;
    dependencyNotice.textContent = missing ? `L2TP 运行组件尚未就绪${Array.isArray(dependencies.missing) && dependencies.missing.length ? `：${dependencies.missing.join('、')}` : ''}。可先保存连接信息，安装所需组件后再连接。` : '';
    dependencyNotice.classList.toggle('hidden', !missing);
    const refresh = $('#refreshL2TPBtn'); refresh.disabled = !!state.l2tpRefreshPromise; refresh.textContent = state.l2tpRefreshPromise ? '↻ 刷新中…' : '↻ 刷新状态'; refresh.setAttribute('aria-busy', String(!!state.l2tpRefreshPromise));
    if (!connections.length) {
      list.innerHTML = `<div class="panel l2tp-empty"><span class="l2tp-empty-icon">⇄</span><h2>${state.l2tpError ? '暂时无法读取连接' : state.l2tpLoaded ? '添加第一条 L2TP 连接' : '正在读取连接状态…'}</h2><p>${state.l2tpError ? '点击“刷新状态”重试，已保存的连接不会因此删除。' : '填写服务器地址、用户名和密码，保存后手动连接。'}</p>${state.l2tpLoaded && !state.l2tpError ? '<button class="btn btn-primary" data-l2tp-action="add" type="button">＋ 添加 L2TP</button>' : ''}</div>`;
      return;
    }
    list.innerHTML = connections.map(connection => {
      const status = connection.status || {}, visual = l2tpStatus(connection), id = String(connection.id);
      const busy = state.l2tpPending.has(id), connected = l2tpOnline(connection), desired = connection.enabled !== false;
      const disabled = busy ? 'disabled' : '', connectDisabled = busy || missing || !!state.l2tpError ? 'disabled' : '';
      const bound = l2tpBoundCount(connection);
      return `<article class="panel l2tp-card ${visual.key === 'online' ? 'l2tp-card-online' : ''}"><div class="l2tp-card-head"><span class="l2tp-card-icon">⇄</span><div class="l2tp-card-title"><h2>${esc(connection.name || connection.address || 'L2TP 连接')}</h2><span class="mono">${esc(connection.address || '—')}</span></div><span class="l2tp-state ${visual.key}" role="status"><i></i>${visual.label}</span></div>${connection.remark ? `<p class="l2tp-remark">${esc(connection.remark)}</p>` : ''}<dl class="l2tp-details"><div><dt>接口</dt><dd class="mono">${esc(status.device || status.interface || '—')}</dd></div><div><dt>隧道 IP</dt><dd class="mono">${esc(status.local_ip || '—')}</dd></div><div><dt>连接时长</dt><dd>${connected && !state.l2tpError ? esc(l2tpDuration(status.uptime)) : '—'}</dd></div><div><dt>绑定设备</dt><dd><button class="text-btn" type="button" data-l2tp-action="devices" data-id="${esc(id)}">${bound} 台 · 查看</button></dd></div></dl>${status.last_error ? `<p class="l2tp-connection-error">${esc(status.last_error)}</p>` : ''}<div class="l2tp-card-actions">${desired || connected || status.state === 'connecting' ? `<button class="btn btn-ghost" type="button" data-l2tp-action="disconnect" data-id="${esc(id)}" ${disabled}>断开</button><button class="btn btn-primary" type="button" data-l2tp-action="reconnect" data-id="${esc(id)}" ${connectDisabled}>重新连接</button>` : `<button class="btn btn-primary" type="button" data-l2tp-action="connect" data-id="${esc(id)}" ${connectDisabled}>连接</button>`}<span class="l2tp-action-spacer"></span><button class="text-btn" type="button" data-l2tp-action="edit" data-id="${esc(id)}" ${disabled}>编辑</button><button class="text-btn" type="button" data-l2tp-action="copy" data-id="${esc(id)}" ${disabled}>复制信息</button><button class="text-btn l2tp-delete" type="button" data-l2tp-action="delete" data-id="${esc(id)}" ${disabled}>删除</button></div>${busy ? '<p class="l2tp-operation" role="status">正在处理，请稍候…</p>' : ''}</article>`;
    }).join('');
  }
  async function refreshL2TP(notify = false) {
    if (state.l2tpRefreshPromise) {
      const ok = await state.l2tpRefreshPromise;
      if (notify) toast(ok ? 'L2TP 状态已刷新' : `刷新失败：${state.l2tpError}`, ok ? 'success' : 'error');
      return ok;
    }
    state.l2tpRefreshPromise = (async () => {
      try {
        const result = await api('/l2tp', { cache: 'no-store' });
        if (!Array.isArray(result?.connections)) throw new Error('服务器返回的连接列表无效');
        state.l2tpConnections = responseList(result, 'connections');
        state.l2tpDependencies = result.dependencies || null;
        state.l2tpError = ''; state.l2tpLoaded = true; state.l2tpUpdatedAt = new Date();
        state.nodes = regularNodes().concat(state.l2tpConnections);
        return true;
      } catch (error) { state.l2tpError = error.message; return false; }
    })();
    renderL2TP();
    const ok = await state.l2tpRefreshPromise;
    state.l2tpRefreshPromise = null;
    renderL2TP();
    if (notify) toast(ok ? `L2TP 状态已刷新：${state.l2tpConnections.filter(l2tpOnline).length} 条在线` : `刷新失败：${state.l2tpError}`, ok ? 'success' : 'error');
    return ok;
  }
  async function refreshL2TPGlobalState() {
    try {
      const [settings, status] = await Promise.all([api('/settings', { cache: 'no-store' }), api('/status', { cache: 'no-store' })]);
      if (!settings || !status || Array.isArray(settings) || Array.isArray(status)) return false;
      state.settings = settings; state.status = status;
      if (settings.enabled != null) state.status.enabled = settings.enabled;
      renderStatus(); renderSettings(); renderDevices();
      return true;
    } catch (_) { return false; }
  }
  function syncL2TPPolling() {
    if (state.standalone || state.page !== 'l2tp' || document.hidden) {
      if (state.l2tpTimer !== null) clearInterval(state.l2tpTimer);
      state.l2tpTimer = null;
      return;
    }
    if (state.l2tpTimer !== null) return;
    refreshL2TP();
    state.l2tpTimer = setInterval(() => {
      if (state.page === 'l2tp' && !document.hidden && state.l2tpPending.size === 0 && !state.l2tpSaving) refreshL2TP();
    }, 5000);
  }
  let l2tpPreviousFocus = null;
  function openL2TPForm(id) {
    const connection = id ? l2tpByID(id) : null;
    if (id && !connection) return;
    state.l2tpEditing = connection ? String(connection.id) : null;
    l2tpPreviousFocus = document.activeElement;
    $('#l2tpModalTitle').textContent = connection ? '编辑 L2TP 连接' : '添加 L2TP 连接';
    $('#l2tpModalHint').textContent = connection?.enabled ? '修改连接参数后会重新连接；已绑定设备在重连期间会暂时断网。' : '保存后不会自动连接，请在连接卡片中点击“连接”。';
    $('#l2tpName').value = connection?.name || '';
    $('#l2tpAddress').value = connection?.address || '';
    $('#l2tpUsername').value = connection?.username || '';
    $('#l2tpPassword').value = ''; $('#l2tpPassword').required = !connection;
    $('#l2tpPasswordHint').textContent = connection ? '留空表示保留已保存的密码。' : '使用服务商提供的 L2TP 密码。';
    $('#l2tpMTU').value = connection?.mtu || 1400;
    $('#l2tpRemark').value = connection?.remark || '';
    $('#l2tpFormError').textContent = ''; $('#l2tpFormError').classList.add('hidden');
    $('#l2tpModalBackdrop').classList.remove('hidden'); $('.app-shell').inert = true;
    $('#l2tpName').focus();
  }
  function closeL2TPForm() {
    if (state.l2tpSaving) return;
    $('#l2tpModalBackdrop').classList.add('hidden'); $('.app-shell').inert = false;
    $('#l2tpPassword').value = ''; state.l2tpEditing = null;
    if (l2tpPreviousFocus?.isConnected) l2tpPreviousFocus.focus();
  }
  async function saveL2TP(event) {
    event.preventDefault();
    if (state.l2tpSaving || !$('#l2tpForm').reportValidity()) return;
    const id = state.l2tpEditing;
    const body = { name: $('#l2tpName').value.trim(), address: $('#l2tpAddress').value.trim(), username: $('#l2tpUsername').value.trim(), remark: $('#l2tpRemark').value.trim(), mtu: Number($('#l2tpMTU').value) };
    const password = $('#l2tpPassword').value;
    if (password || !id) body.password = password;
    if (!id) body.enabled = false;
    if (!body.name || !body.address || !body.username) { $('#l2tpFormError').textContent = '请填写连接名称、服务器地址和用户名。'; $('#l2tpFormError').classList.remove('hidden'); return; }
    state.l2tpSaving = true; $('#saveL2TPBtn').disabled = true; $('#saveL2TPBtn').textContent = '保存中…';
    $('#l2tpModalClose').disabled = true; $('#l2tpModalCancel').disabled = true;
    let saved = false;
    try {
      if (state.l2tpRefreshPromise) await state.l2tpRefreshPromise;
      await api(`/l2tp${id ? `/${encodeURIComponent(id)}` : ''}`, { method: id ? 'PATCH' : 'POST', body });
      saved = true; state.l2tpSaving = false; closeL2TPForm();
      const refreshed = await refreshL2TP();
      toast(refreshed ? id ? 'L2TP 连接已保存' : 'L2TP 已添加，点击“连接”后即可供设备选择' : '连接已保存，但状态刷新失败，请手动刷新', refreshed ? 'success' : 'error');
    } catch (error) {
      if (!saved) { $('#l2tpFormError').textContent = `保存失败：${error.message}`; $('#l2tpFormError').classList.remove('hidden'); }
      else toast(`连接已保存，但刷新失败：${error.message}`, 'error');
    } finally {
      state.l2tpSaving = false; $('#saveL2TPBtn').disabled = false; $('#saveL2TPBtn').textContent = '保存连接';
      $('#l2tpModalClose').disabled = false; $('#l2tpModalCancel').disabled = false;
    }
  }
  async function runL2TPAction(action, id) {
    if (action === 'add') { openL2TPForm(); return; }
    const connection = l2tpByID(id);
    if (!connection || state.l2tpPending.has(String(id))) return;
    if (action === 'edit') { openL2TPForm(id); return; }
    if (action === 'devices') {
      try {
        const devices = responseList(await api('/devices?hidden=all', { cache: 'no-store' }), 'devices');
        state.allDevices = devices;
        const bound = devices.filter(device => String(device.node_id || '') === String(id));
        connection.bound_devices = bound.length; renderL2TP();
        const lines = bound.map(device => `• ${device.remark || device.hostname || '未命名设备'}${device.hidden ? '（隐藏）' : ''} · ${device.online ? '在线' : '离线'}\n  ${device.ip || 'IP 未知'} · ${device.mac || 'MAC 未知'}`);
        await showActionDialog({ title: '绑定这条 L2TP 的设备', message: `${connection.name || connection.address}\n\n${lines.length ? lines.join('\n\n') : '尚无设备绑定此连接。'}\n\n可在设备管理中调整绑定。`, submitText: '知道了' });
      } catch (error) { toast(`读取绑定设备失败：${error.message}`, 'error'); }
      return;
    }
    if (action === 'copy') {
      const status = connection.status || {};
      const info = [`名称：${connection.name || ''}`, '协议：L2TP', `服务器：${connection.address || ''}`, `状态：${l2tpStatus(connection).label}`, `接口：${status.device || status.interface || '—'}`, `隧道 IP：${status.local_ip || '—'}`, connection.remark ? `备注：${connection.remark}` : ''].filter(Boolean).join('\n');
      const copied = await copyText(info);
      toast(copied ? '连接信息已复制（不含账号密码）' : '复制失败，请检查浏览器剪贴板权限', copied ? 'success' : 'error'); return;
    }
    if (!['connect', 'disconnect', 'reconnect', 'delete'].includes(action)) return;
    state.l2tpPending.add(String(id)); renderL2TP();
    try {
      if (action === 'delete') {
        const devices = responseList(await api('/devices?hidden=all', { cache: 'no-store' }), 'devices');
        state.allDevices = devices;
        const bound = devices.filter(device => String(device.node_id || '') === String(id));
        if (bound.length) { await showNodeBindings(id, bound); return; }
        const confirmed = await showActionDialog({ title: '删除 L2TP 连接', message: `确认删除“${connection.name || connection.address}”？此操作会断开连接并删除保存的账号信息。`, submitText: '确认删除', danger: true });
        if (!confirmed) return;
      } else if (action === 'disconnect' || action === 'reconnect') {
        const confirmed = await showActionDialog({ title: action === 'disconnect' ? '断开 L2TP 连接' : '重新连接 L2TP', message: `“${connection.name || connection.address}”当前绑定 ${l2tpBoundCount(connection)} 台设备。${action === 'disconnect' ? '断开后' : '重新连接期间'}，这些设备会断网，不会回退到本地直连。设备绑定会保留。`, submitText: action === 'disconnect' ? '确认断开' : '重新连接', danger: action === 'disconnect' });
        if (!confirmed) return;
      }
      if (state.l2tpRefreshPromise) await state.l2tpRefreshPromise;
      await api(`/l2tp/${encodeURIComponent(id)}${action === 'delete' ? '' : `/${action}`}`, { method: action === 'delete' ? 'DELETE' : 'POST' });
      const refreshed = await refreshL2TP();
      const message = { connect: '已发起 L2TP 连接，在线状态会自动更新', disconnect: '连接已断开，设备绑定已保留', reconnect: '已发起重新连接，请等待状态更新', delete: 'L2TP 连接已删除' }[action];
      const globalRefreshed = action === 'connect' || action === 'reconnect' ? await refreshL2TPGlobalState() : true;
      toast(refreshed && globalRefreshed ? message : `${message}；但${!globalRefreshed ? '全局' : '连接'}状态刷新失败，请刷新页面`, refreshed && globalRefreshed ? 'success' : 'error');
    } catch (error) {
      if (error.status === 409 && action === 'delete') await showNodeBindings(id, responseList(error.data, 'bound_devices'), error.message);
      else toast(`操作失败：${error.message}`, 'error');
      await refreshL2TP();
      if (action === 'connect' || action === 'reconnect') await refreshL2TPGlobalState();
    } finally { state.l2tpPending.delete(String(id)); renderL2TP(); }
  }
  function setupL2TPEvents() {
    $('#addL2TPBtn')?.addEventListener('click', () => openL2TPForm());
    $('#refreshL2TPBtn')?.addEventListener('click', () => refreshL2TP(true));
    $('#l2tpList')?.addEventListener('click', event => { const button = event.target.closest('[data-l2tp-action]'); if (button && !button.disabled) runL2TPAction(button.dataset.l2tpAction, button.dataset.id); });
    $('#l2tpForm')?.addEventListener('submit', saveL2TP);
    $('#l2tpModalClose')?.addEventListener('click', closeL2TPForm); $('#l2tpModalCancel')?.addEventListener('click', closeL2TPForm);
    $('#l2tpModalBackdrop')?.addEventListener('click', event => { if (event.target.id === 'l2tpModalBackdrop') closeL2TPForm(); });
    document.addEventListener('visibilitychange', syncL2TPPolling);
    document.addEventListener('keydown', event => {
      if ($('#l2tpModalBackdrop').classList.contains('hidden')) return;
      if (event.key === 'Escape') { event.preventDefault(); closeL2TPForm(); }
      if (event.key === 'Tab') {
        const controls = $$('button:not(:disabled), input:not(:disabled), textarea:not(:disabled)', $('#l2tpModalBackdrop'));
        const first = controls[0], last = controls[controls.length - 1];
        if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
      }
    });
  }
  function ensureNodeTestControls() {
    const toolbar = $('.node-toolbar');
    if (!toolbar) return;
    if ($('#urlTestRegion')) {
      if (document.activeElement !== $('#urlTestAddress')) $('#urlTestAddress').value = state.settings.url_test_address || 'https://www.gstatic.com/generate_204';
      $('#urlTestRegion').value = state.settings.url_test_region || 'overseas';
      return;
    }
    const wrap = document.createElement('div');
    wrap.className = 'node-test-controls';
    wrap.innerHTML = '<span>URL 测试</span><select class="select compact" id="urlTestRegion" aria-label="URL 测试区域"><option value="overseas">海外</option><option value="domestic">国内</option></select><input class="input compact-input" id="urlTestAddress" aria-label="URL 测试地址" placeholder="https://…" /><button class="text-btn" id="saveTestAddressBtn">保存地址</button>';
    toolbar.insertBefore(wrap, toolbar.querySelector('.search-box'));
    const region = $('#urlTestRegion');
    const input = $('#urlTestAddress');
    const addresses = { overseas: 'https://www.gstatic.com/generate_204', domestic: 'https://www.baidu.com' };
    region.value = state.settings.url_test_region || 'overseas';
    input.value = state.settings.url_test_address || addresses[region.value];
    region.addEventListener('change', () => { input.value = addresses[region.value]; state.settings.url_test_region = region.value; state.settings.url_test_address = input.value; });
    input.addEventListener('input', () => { state.settings.url_test_address = input.value.trim(); });
    $('#saveTestAddressBtn').addEventListener('click', async () => {
      try {
        const url = new URL(input.value.trim());
        if (!['https:', 'http:'].includes(url.protocol)) throw new Error('请输入 HTTP 或 HTTPS 地址');
        await api('/settings', { method: 'PUT', body: { url_test_address: input.value.trim(), url_test_region: region.value } });
        toast('测试地址已保存');
      } catch (error) { toast(`保存失败：${error.message}`, 'error'); }
    });
  }
  function renderDns() { const current = state.settings.default_dns || 'cloudflare'; $$('input[name="defaultDns"]').forEach(input => { input.checked = input.value === current; input.closest('.dns-option')?.classList.toggle('selected', input.checked); }); $('#forceDoh').checked = state.settings.force_doh !== false; $('#proxyDns').checked = state.settings.proxy_dns !== false; $('#dnsFailClosed').checked = state.settings.dns_fail_closed !== false; $('#customDnsInput').value = state.settings.custom_dns || ''; $('#customDnsWrap').classList.toggle('hidden', current !== 'custom'); }
  function renderSettings() { const s = state.settings; $('#killSwitch').checked = s.kill_switch !== false; $('#newDevicePolicy').value = s.default_mode || 'direct'; $('#selfServiceEnabled').checked = s.self_service_enabled !== false; $('#hideAp').checked = s.hide_ap !== false; const running = state.status.kernel_running === true; $('#serviceUptime').textContent = running && state.status.uptime ? `运行 ${state.status.uptime}` : ''; $('#serviceRunning').textContent = running ? '内核运行中' : '内核未运行'; $('#serviceRunning').classList.toggle('warning', !running); $('#serviceSummary').textContent = s.enabled === false ? '全局保护当前关闭，代理绑定未生效。选择代理节点会自动开启并应用。' : running ? '代理配置已加载，设备绑定后自动应用。' : '代理绑定当前未生效，请重载内核并检查错误提示。'; $('.service-state .status-dot')?.classList.toggle('stopped', !running); if ($('#selfServiceUrl')) $('#selfServiceUrl').textContent = selfURL(); if ($('.self-url span')) $('.self-url span').textContent = selfURL(); }

  function bindingNotice(device, enabled, kernelRunning) {
    if (device?.mode !== 'proxy') return { message: '设备设置已应用', type: 'success' };
    if (enabled !== true || kernelRunning !== true) return { message: enabled === false ? '设备设置已保存，但全局保护关闭，代理未生效' : kernelRunning === false ? '设备设置已保存，但代理内核未运行，代理未生效' : '设备设置已保存，暂时无法确认代理运行状态，请刷新检查', type: 'error' };
    return { message: '代理绑定已应用，全局保护和代理内核已开启', type: 'success' };
  }
  async function patchDevice(id, body) {
    let saved = false;
    try {
      if (!state.demo) await api(`/devices/${encodeURIComponent(id)}`, { method: 'PATCH', body });
      else { const device = state.allDevices.find(x => String(x.id) === String(id)); if (device) Object.assign(device, body); }
      saved = true;
      state.pendingProxy.delete(String(id));
      if (!state.demo) await loadData(true);
      else { renderStatus(); renderDevices(); }
      const device = state.allDevices.find(x => String(x.id) === String(id));
      const notice = body.mode || body.node_id || body.dns ? bindingNotice(device, state.settings.enabled, state.status.kernel_running) : { message: body.remark != null ? (device?.remark ? '设备备注已保存，自助页可见' : '设备备注已清除') : '设备设置已保存', type: 'success' };
      toast(notice.message, notice.type);
    } catch (error) {
      if (!state.demo) { try { await loadData(true); } catch (_) { /* Keep the original save/apply error. */ } }
      renderDevices();
      toast(`${saved ? '设置已保存，但刷新运行状态失败' : '保存或应用失败'}：${error.message}`, 'error');
    }
  }
  async function editDeviceRemark(id) {
    const device = state.allDevices.find(x => String(x.id) === String(id));
    if (!device) return;
    const value = await showActionDialog({ title: '编辑设备备注', message: '最多 200 字，设备自助页可见；留空可清除。', label: '设备备注', value: device.remark || '' });
    if (value === null) return;
    if (Array.from(value.trim()).length > 200) { toast('设备备注不能超过 200 字', 'error'); return; }
    patchDevice(id, { remark: value.trim() });
  }
  async function releaseDevice(id) {
    const device = state.allDevices.find(x => String(x.id) === String(id));
    if (!device) return;
    const confirmed = await showActionDialog({ title: '释放设备', message: `释放“${device.remark || device.hostname || device.ip}”？\n将清除该设备的 OpenPass 记录、备注和节点绑定。在线设备可能立即重新出现，并使用新设备默认策略（${state.settings.default_mode === 'blocked' ? '禁止网络' : '本地直连'}）。\n此操作不会释放路由器的 DHCP 租约。`, submitText: '确认释放', danger: true });
    if (!confirmed) return;
    let released = false;
    try {
      if (!state.demo) await api(`/devices/${encodeURIComponent(id)}`, { method: 'DELETE' });
      released = true;
      state.pendingProxy.delete(String(id));
      if (!state.demo) await loadData(true);
      else { state.allDevices = state.allDevices.filter(d => String(d.id) !== String(id)); renderAll(); }
      toast('设备记录和绑定已释放；重新发现时使用新设备默认策略');
    } catch (error) { toast(`${released ? '设备已释放，但刷新失败' : '释放失败'}：${error.message}`, 'error'); }
  }
  async function refreshDevices() {
    const button = $('#discoverBtn');
    if (!button || button.disabled) return;
    const label = button.innerHTML;
    button.disabled = true;
    button.setAttribute('aria-busy', 'true');
    button.innerHTML = '↻ 刷新中…';
    try {
      const payload = state.demo ? state.allDevices : await api('/devices?hidden=all');
      const all = responseList(payload, 'devices');
      state.allDevices = all;
      state.devices = all.filter(device => !device.hidden);
      renderStatus();
      renderDashboard();
      renderDevices();
      const online = all.filter(device => device.online && !device.hidden).length;
      toast(`设备已刷新：发现 ${all.length} 台，在线 ${online} 台`);
    } catch (error) {
      toast(`刷新设备失败：${error.message}`, 'error');
    } finally {
      button.disabled = false;
      button.removeAttribute('aria-busy');
      button.innerHTML = label;
    }
  }
  async function testNode(id, type = 'url') {
    const node = findNode(id);
    if (!node || isL2TP(node) || node.testing) return;
    const testURL = $('#urlTestAddress')?.value.trim() || state.settings.url_test_address || 'https://www.gstatic.com/generate_204';
    node.testing = type;
    renderNodes();
    try {
      const result = await api(`/nodes/${encodeURIComponent(id)}/test`, { method: 'POST', body: { type, url: testURL } });
      node.test_results = { ...node.test_results, [type]: result };
      node.last_test = new Date().toLocaleTimeString('zh-CN');
      toast(result.ok ? `${testLabels[type]} 通过，${result.latency_ms ?? '—'} ms` : `${testLabels[type]} 失败：${result.error || result.note || '目标未响应'}`, result.ok ? 'success' : 'error');
    } catch (error) {
      node.test_results = { ...node.test_results, [type]: { ok: false, error: error.message } };
      toast(`${testLabels[type]} 检测失败：${error.message}`, 'error');
    } finally {
      node.testing = false;
      renderNodes();
      renderStatus();
      renderDashboard();
    }
  }
  async function testAllNodes() {
    const button = $('#testAllBtn');
    button.disabled = true;
    try { for (const node of regularNodes()) await testNode(node.id, 'url'); }
    finally { button.disabled = false; }
  }
  async function saveSettings() { const body = { kill_switch: $('#killSwitch').checked, default_mode: $('#newDevicePolicy').value, self_service_enabled: $('#selfServiceEnabled').checked, hide_ap: $('#hideAp').checked }; try { if (!state.demo) { await api('/settings', { method: 'PUT', body }); await loadData(true); } else { state.settings = { ...state.settings, ...body }; renderStatus(); } toast('系统设置已保存'); } catch (error) { toast(`保存或刷新失败：${error.message}`, 'error'); } }
  async function saveDns() { const selected = $('input[name="defaultDns"]:checked')?.value || 'cloudflare'; const body = { default_dns: selected, custom_dns: $('#customDnsInput').value.trim(), force_doh: $('#forceDoh').checked, proxy_dns: $('#proxyDns').checked, dns_fail_closed: $('#dnsFailClosed').checked }; try { if (!state.demo) { await api('/settings', { method: 'PUT', body }); await loadData(true); } else { state.settings = { ...state.settings, ...body }; renderStatus(); } toast('DNS 防泄露设置已保存'); } catch (error) { toast(`保存或刷新失败：${error.message}`, 'error'); } }
  async function importNodes() {
    const subscription = !$('#importSubscriptionPane').classList.contains('hidden');
    const content = subscription ? '' : $('#nodeImportText').value.trim();
    const url = subscription ? $('#subscriptionUrl').value.trim() : '';
    const name = subscription ? $('#subscriptionName').value.trim() : '';
    const remark = subscription ? $('#subscriptionRemark').value.trim() : '';
    if (!content && !url) { toast('请粘贴节点内容或订阅地址', 'error'); return; }
    if (subscription && !name) { toast('请填写订阅名称，方便在节点列表中区分', 'error'); $('#subscriptionName').focus(); return; }
    let imported = null;
    try {
      const result = state.demo ? { imported: content.split(/\r?\n/).filter(Boolean).length || 3 } : subscription ? await api('/subscriptions', { method: 'POST', body: { url, name, remark } }) : await api('/nodes/import', { method: 'POST', body: { content, url } });
      imported = result?.imported ?? responseList(result, 'nodes').length;
      closeModal();
      if (!state.demo) {
        if (imported > 0) await api('/settings', { method: 'PUT', body: { enabled: true } });
        await api('/apply', { method: 'POST' });
        await loadData(true);
      }
      const inactive = imported > 0 && state.status.kernel_running !== true;
      toast(`成功导入 ${imported} 个节点${inactive ? '，但代理内核未运行，请检查配置' : '，配置已应用'}`, inactive ? 'error' : 'success');
    } catch (error) {
      if (!state.demo) { try { await loadData(true); } catch (_) { /* Keep the original import/apply error. */ } }
      toast(`${imported === null ? '导入失败' : `已导入 ${imported} 个节点，但应用或刷新失败`}：${error.message}`, 'error');
    }
  }

  function subscriptionByID(id) { return state.subscriptions.find(item => String(item.id) === String(id)); }
  async function selectSubscription(id) {
    state.nodeSubscriptionFilter = state.nodeSubscriptionFilter === String(id) ? 'all' : String(id);
    renderSubscriptions(); renderNodes();
  }
  async function refreshSubscription(id) {
    const subscription = subscriptionByID(id);
    if (!subscription) return;
    try {
      const result = state.demo ? { imported: 0 } : await api(`/subscriptions/${encodeURIComponent(id)}/refresh`, { method: 'POST' });
      await loadData(true);
      toast(`订阅“${subscription.name || '未命名订阅'}”已刷新${result?.imported != null ? `，同步 ${result.imported} 个节点` : ''}`);
    } catch (error) {
      if (error.status === 409 && error.data?.bound_devices) {
        await showActionDialog({ title: '订阅包含已绑定节点', message: `${error.message}\n请先解绑这些设备，再刷新订阅。`, submitText: '知道了' });
      } else toast(`刷新订阅失败：${error.message}`, 'error');
    }
  }
  async function editSubscription(id) {
    const subscription = subscriptionByID(id);
    if (!subscription) return;
    const name = await showActionDialog({ title: '编辑订阅名称', message: '名称会显示在订阅列表和节点筛选中。', label: '订阅名称', value: subscription.name || '', submitText: '下一步', maxLength: 100 });
    if (name === null) return;
    if (!name.trim()) { toast('订阅名称不能为空', 'error'); return; }
    const remark = await showActionDialog({ title: '编辑订阅备注', message: '备注用于记录来源、地区或用途；留空可清除。', label: '订阅备注', value: subscription.remark || '', submitText: '保存', maxLength: 200 });
    if (remark === null) return;
    try {
      if (!state.demo) await api(`/subscriptions/${encodeURIComponent(id)}`, { method: 'PATCH', body: { name: name.trim(), remark: remark.trim() } });
      Object.assign(subscription, { name: name.trim(), remark: remark.trim() });
      renderSubscriptions(); renderNodes();
      toast('订阅信息已保存');
    } catch (error) { toast(`保存订阅信息失败：${error.message}`, 'error'); }
  }
  async function deleteSubscription(id) {
    const subscription = subscriptionByID(id);
    if (!subscription) return;
    const confirmed = await showActionDialog({ title: '删除订阅', message: `删除“${subscription.name || '未命名订阅'}”后，会同时删除该订阅导入的节点。已绑定设备必须先解绑，否则删除会被拒绝。`, submitText: '确认删除', danger: true });
    if (!confirmed) return;
    try {
      if (!state.demo) await api(`/subscriptions/${encodeURIComponent(id)}`, { method: 'DELETE' });
      state.subscriptions = state.subscriptions.filter(item => String(item.id) !== String(id));
      state.nodes = state.nodes.filter(node => nodeSubscriptionID(node) !== String(id));
      if (state.nodeSubscriptionFilter === String(id)) state.nodeSubscriptionFilter = 'all';
      renderSubscriptions(); renderNodes();
      if (!state.demo) await loadData(true);
      toast(`订阅“${subscription.name || '未命名订阅'}”已删除`);
    } catch (error) {
      if (error.status === 409) await showActionDialog({ title: '订阅仍被设备使用', message: `${error.message}\n请先在设备管理中解绑这些设备，再删除订阅。`, submitText: '知道了' });
      else toast(`删除订阅失败：${error.message}`, 'error');
    }
  }

  function exportNodes(format) {
    if (state.demo) { toast('演示模式不提供导出', 'error'); return; }
    const link = document.createElement('a'); link.href = `${API}/nodes/export?format=${encodeURIComponent(format || 'uri')}`; link.download = format === 'json' ? 'openpass-nodes.json' : 'openpass-nodes.txt'; document.body.appendChild(link); link.click(); link.remove(); toast('节点导出已开始');
  }

  async function fetchNodeURI(id) {
    if (state.demo) throw new Error('演示模式不提供导出');
    const response = await fetch(`${API}/nodes/${encodeURIComponent(id)}/export?format=uri`, { credentials: 'include', cache: 'no-store' });
    const text = await response.text();
    if (!response.ok) {
      let message = text.trim() || `HTTP ${response.status}`;
      try { const data = JSON.parse(text); message = data.error || data.message || message; } catch (_) { /* plain-text error */ }
      throw new Error(message);
    }
    const uri = text.trim();
    if (!uri) throw new Error('节点没有可导出的链接');
    return uri;
  }

  async function copyText(value) {
    try {
      if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
        await navigator.clipboard.writeText(value);
        return true;
      }
    } catch (_) { /* HTTP pages may not expose navigator.clipboard. */ }
    const input = document.createElement('textarea');
    input.value = value;
    input.setAttribute('readonly', '');
    input.style.position = 'fixed'; input.style.opacity = '0'; input.style.pointerEvents = 'none';
    document.body.appendChild(input); input.select(); input.setSelectionRange(0, input.value.length);
    let copied = false;
    try { copied = document.execCommand('copy'); } catch (_) { copied = false; }
    input.remove();
    return copied;
  }

  async function copyNodeURI(id) {
    try {
      const uri = await fetchNodeURI(id);
      if (!await copyText(uri)) throw new Error('浏览器禁止访问剪贴板，请手动导出链接');
      toast('节点链接已复制');
    } catch (error) { toast(`复制失败：${error.message}`, 'error'); }
  }

  async function exportNodeURI(id) {
    try {
      const uri = await fetchNodeURI(id);
      const node = findNode(id);
      const label = (node?.remark || node?.name || node?.address || 'openpass-node').replace(/[\\/:*?"<>|\s]+/g, '-').replace(/^-+|-+$/g, '') || 'openpass-node';
      const blob = new Blob([`${uri}\n`], { type: 'text/plain;charset=utf-8' });
      const link = document.createElement('a'); link.href = URL.createObjectURL(blob); link.download = `${label}.txt`; document.body.appendChild(link); link.click(); link.remove();
      setTimeout(() => URL.revokeObjectURL(link.href), 1000);
      toast('节点链接导出已开始');
    } catch (error) { toast(`导出失败：${error.message}`, 'error'); }
  }

  async function editNodeRemark(id) {
    const node = findNode(id); if (!node) return;
    const value = await showActionDialog({ title: '编辑节点备注', message: '最多 200 字，会显示在设备自助页的节点选项中；留空可清除。', label: '节点备注', value: node.remark || '' });
    if (value === null) return;
    try {
      const updated = state.demo ? { ...node, remark: value.trim() } : await api(`/nodes/${encodeURIComponent(id)}`, { method: 'PATCH', body: { remark: value.trim() } });
      Object.assign(node, updated || { remark: value.trim() });
      renderNodes();
      toast(value.trim() ? '节点备注已保存' : '节点备注已清除');
    } catch (error) { toast(`备注保存失败：${error.message}`, 'error'); }
  }

  function openModal() { $('#modalBackdrop').classList.remove('hidden'); $('#nodeImportText').focus(); }
  function closeModal() { $('#modalBackdrop').classList.add('hidden'); $('#subscriptionUrl').value = ''; $('#subscriptionName').value = ''; $('#subscriptionRemark').value = ''; }
  function setupEvents() {
    window.addEventListener('hashchange', () => navigate(location.hash.slice(1) || 'dashboard'));
    $$('.nav-item').forEach(el => el.addEventListener('click', e => { e.preventDefault(); navigate(el.dataset.page); }));
    $$('[data-goto]').forEach(el => el.addEventListener('click', () => navigate(el.dataset.goto)));
    $('#menuBtn')?.addEventListener('click', () => $('#sidebar').classList.toggle('open')); $('#refreshBtn')?.addEventListener('click', () => loadData()); $('#discoverBtn')?.addEventListener('click', refreshDevices); $('#importNodeBtn')?.addEventListener('click', openModal); $('#exportNodesBtn')?.addEventListener('click', () => exportNodes('uri')); $('#exportNodesJsonBtn')?.addEventListener('click', () => exportNodes('json')); $('#modalClose')?.addEventListener('click', closeModal); $('#modalCancel')?.addEventListener('click', closeModal); $('#modalBackdrop')?.addEventListener('click', e => { if (e.target.id === 'modalBackdrop') closeModal(); }); $('#importSubmit')?.addEventListener('click', importNodes);
    $$('.import-tab').forEach(tab => tab.addEventListener('click', () => { $$('.import-tab').forEach(t => t.classList.toggle('active', t === tab)); $('#importTextPane').classList.toggle('hidden', tab.dataset.importTab !== 'text'); $('#importSubscriptionPane').classList.toggle('hidden', tab.dataset.importTab !== 'subscription'); if (tab.dataset.importTab === 'subscription') $('#subscriptionName').focus(); }));
    $('#subscriptionsList')?.addEventListener('click', e => { const el = e.target.closest('[data-action]'); if (!el) return; const id = el.dataset.id; if (el.dataset.action === 'select-subscription') selectSubscription(id); if (el.dataset.action === 'refresh-subscription') refreshSubscription(id); if (el.dataset.action === 'edit-subscription') editSubscription(id); if (el.dataset.action === 'delete-subscription') deleteSubscription(id); });
    $('#nodeSubscriptionClear')?.addEventListener('click', () => { state.nodeSubscriptionFilter = 'all'; renderSubscriptions(); renderNodes(); });
    $$('[data-device-tab]').forEach(tab => tab.addEventListener('click', () => { $$('[data-device-tab]').forEach(t => t.classList.toggle('active', t === tab)); state.deviceTab = tab.dataset.deviceTab; renderDevices(); })); $('#deviceSearch')?.addEventListener('input', renderDevices); $('#showHiddenBtn')?.addEventListener('click', () => { state.deviceTab = 'hidden'; $$('[data-device-tab]').forEach(t => t.classList.toggle('active', t.dataset.deviceTab === 'hidden')); renderDevices(); });
    $('#devicesTable')?.addEventListener('change', e => {
      const el = e.target; const id = el.dataset.id; if (!id) return;
      if (el.dataset.action === 'mode') {
        const device = state.allDevices.find(d => String(d.id) === String(id));
        if (el.value === 'proxy' && !device?.node_id) {
          state.pendingProxy.add(String(id)); renderDevices();
          toast('请选择代理节点，选择后将立即绑定');
        } else {
          state.pendingProxy.delete(String(id));
          patchDevice(id, { mode: el.value, node_id: el.value === 'proxy' ? device.node_id : '' });
        }
      }
      if (el.dataset.action === 'node') { if (el.value) patchDevice(id, { node_id: el.value, mode: 'proxy' }); else toast('请选择一个代理节点', 'error'); }
      if (el.dataset.action === 'dns') patchDevice(id, { dns: el.value });
    });
    $('#devicesTable')?.addEventListener('click', e => { const el = e.target.closest('[data-action]'); if (!el) return; if (el.dataset.action === 'hide') patchDevice(el.dataset.id, { hidden: !state.allDevices.find(d => String(d.id) === String(el.dataset.id))?.hidden }); if (el.dataset.action === 'block') patchDevice(el.dataset.id, { mode: 'blocked', node_id: '' }); if (el.dataset.action === 'remark-device') editDeviceRemark(el.dataset.id); if (el.dataset.action === 'release-device') releaseDevice(el.dataset.id); });
    $$('[data-node-filter]').forEach(tab => tab.addEventListener('click', () => { $$('[data-node-filter]').forEach(t => t.classList.toggle('active', t === tab)); state.nodeFilter = tab.dataset.nodeFilter; renderNodes(); })); $('#nodeSearch')?.addEventListener('input', renderNodes); $('#nodesTable')?.addEventListener('click', e => { const el = e.target.closest('[data-action]'); if (!el || el.dataset.testType) return; if (el.dataset.action === 'test-node') testNode(el.dataset.id); if (el.dataset.action === 'remark-node') editNodeRemark(el.dataset.id); if (el.dataset.action === 'copy-node') copyNodeURI(el.dataset.id); if (el.dataset.action === 'export-node') exportNodeURI(el.dataset.id); if (el.dataset.action === 'delete-node') deleteNode(el.dataset.id); }); $('#testAllBtn')?.addEventListener('click', testAllNodes);
    $$('input[name="defaultDns"]').forEach(radio => radio.addEventListener('change', () => { $$('.dns-option').forEach(option => option.classList.toggle('selected', option.querySelector('input').checked)); $('#customDnsWrap').classList.toggle('hidden', radio.value !== 'custom' || !radio.checked); })); $('#saveDnsBtn')?.addEventListener('click', saveDns); $('#testDnsBtn')?.addEventListener('click', () => { $('#dnsLastTest').textContent = '尚未完成终端检测'; toast('请使用连接本路由器的设备进行 DNS 泄漏检测，配置状态不能替代实际检测。', 'error'); }); $('#saveSettingsBtn')?.addEventListener('click', saveSettings); $('#quickProtect')?.addEventListener('click', async () => { const enabled = !state.settings.enabled; try { if (!state.demo) { await api('/settings', { method: 'PUT', body: { enabled } }); await loadData(true); } else { state.settings.enabled = enabled; renderAll(); } toast(state.settings.enabled ? (state.status.kernel_running === true ? '全局保护已开启，内核运行中' : '全局保护已开启，但内核未运行，请检查配置') : '全局保护已关闭', state.settings.enabled && state.status.kernel_running !== true ? 'error' : 'success'); } catch (error) { toast(`操作或刷新失败：${error.message}`, 'error'); } }); $('#restartCoreBtn')?.addEventListener('click', async () => { try { if (!state.demo) { await api('/apply', { method: 'POST' }); await loadData(true); } toast(state.status.kernel_running === true ? 'sing-box 内核已重载' : '配置已应用，代理内核当前未运行', state.status.kernel_running === true ? 'success' : 'error'); } catch (error) { toast(`重启或刷新失败：${error.message}`, 'error'); } }); $('#openSelfServiceBtn')?.addEventListener('click', () => window.open('/choose', '_blank')); $('#copySelfUrl')?.addEventListener('click', () => navigator.clipboard?.writeText(selfURL()).then(() => toast('自助页地址已复制')));

  }
  async function showNodeBindings(id, devices, message) {
    const node = findNode(id);
    const lines = devices.map(device => {
      const identity = [device.remark, device.hostname, device.ip].filter(Boolean).join(' · ') || device.mac || '未命名设备';
      const status = [device.online === false ? '离线' : device.online === true ? '在线' : '', device.hidden ? '隐藏设备' : ''].filter(Boolean).join('、');
      return `• ${identity}${status ? `（${status}）` : ''}`;
    });
    await showActionDialog({ title: '节点仍被设备绑定', message: `${node ? `“${nodeLabel(node)}”\n` : ''}${message || `该节点仍被 ${devices.length} 台设备绑定，请先解绑设备后再删除。`}${lines.length ? `\n\n${lines.join('\n')}` : ''}\n\n请在设备管理中将这些设备改为直连、禁止网络或其他节点，再删除本节点。`, submitText: '知道了' });
  }
  async function deleteNode(id) {
    const key = String(id);
    if (state.deletingNodes.has(key)) return;
    state.deletingNodes.add(key);
    renderNodes();
    let deleted = false;
    try {
      const devices = state.demo ? state.allDevices : responseList(await api('/devices?hidden=all', { cache: 'no-store' }), 'devices');
      state.allDevices = devices;
      state.devices = devices.filter(device => !device.hidden);
      renderStatus(); renderDevices();
      const bound = devices.filter(device => String(device.node_id || '') === key);
      if (bound.length) { await showNodeBindings(id, bound); return; }
      if (!state.demo) await api(`/nodes/${encodeURIComponent(id)}`, { method: 'DELETE' });
      deleted = true;
      if (!state.demo) await loadData(true);
      else { state.nodes = state.nodes.filter(node => String(node.id) !== key); renderAll(); }
      toast('节点已删除');
    } catch (error) {
      if (error.status === 409) await showNodeBindings(id, responseList(error.data, 'bound_devices'), error.message);
      else toast(`${deleted ? '节点已删除，但刷新页面数据失败' : '删除失败'}：${error.message}`, 'error');
    } finally {
      state.deletingNodes.delete(key);
      renderNodes();
    }
  }
  function setupSelfEvents() {
    $$('.self-choice').forEach(choice => choice.addEventListener('click', () => setSelfMode(choice.dataset.selfMode)));
    $('#selfRefreshBtn').addEventListener('click', loadSelf);
    $('#selfConfirmBtn').addEventListener('click', async () => {
      const mode = state.selfMode;
      const node_id = mode === 'proxy' ? $('#selfNodeSelect').value : '';
      if (!mode) { toast('请选择本地直连或代理节点', 'error'); return; }
      if (mode === 'proxy' && !node_id) { toast('请选择一个代理节点', 'error'); return; }
      $('#selfConfirmBtn').disabled = true;
      $('#selfError').classList.add('hidden');
      try {
        await api('/self', { method: 'POST', body: { mode, node_id } });
        await loadSelf();
        const notice = state.self ? bindingNotice(state.self.device, state.self.enabled ?? state.self.protection_enabled, state.self.kernel_running) : { message: '连接方式已保存，但设备状态刷新失败，请重新刷新检查', type: 'error' };
        toast(notice.message, notice.type);
      } catch (error) {
        await loadSelf();
        $('#selfError').textContent = `保存失败：${error.message}`;
        $('#selfError').classList.remove('hidden');
        toast(`保存失败：${error.message}`, 'error');
      }
    });
  }
  function setupNodeTestTypeEvents() {
    $('#nodesTable')?.addEventListener('click', e => { const button = e.target.closest('[data-test-type]'); if (button) testNode(button.dataset.id, button.dataset.testType); });
  }

  setupActionDialogEvents();
  setupEvents();
  setupSelfEvents();
  setupNodeTestTypeEvents();
  setupL2TPEvents();
  state.page = location.hash.slice(1) || 'dashboard';
  bootstrap();
})();
