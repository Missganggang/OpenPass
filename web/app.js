/* OpenPass admin UI: plain browser JavaScript, no runtime dependencies. */
(function () {
  'use strict';
  const API = '/api';
  const $ = (s, root = document) => root.querySelector(s);
  const $$ = (s, root = document) => Array.from(root.querySelectorAll(s));
  const state = {
    page: 'dashboard', deviceTab: 'online', nodeFilter: 'all',
    devices: [], allDevices: [], nodes: [], settings: {}, dns: [], status: {},
    standalone: /^\/(choose|self)\/?$/.test(location.pathname),
    self: null, selfMode: null, selfLoading: false, pendingProxy: new Set(),
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
    if (!response.ok) { const error = new Error(data.error || data.message || text.trim() || `HTTP ${response.status}`); error.status = response.status; throw error; }
    return data;
  }

  function toast(message, type = 'success') {
    const stack = $('#toastStack'); if (!stack) return;
    const item = document.createElement('div'); item.className = `toast ${type}`; item.textContent = message; stack.appendChild(item);
    setTimeout(() => item.remove(), 3300);
  }
  function esc(value) { return String(value == null ? '' : value).replace(/[&<>'"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[c])); }
  function findNode(id) { return state.nodes.find(n => String(n.id) === String(id)); }
  function nodeLabel(node) { return [node?.name || node?.address || node?.type || '代理节点', node?.remark].filter(Boolean).join(' · '); }
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
      const [devices, allDevices, nodes, settings, dns, status] = await Promise.all([api('/devices'), api('/devices?hidden=all').catch(() => null), api('/nodes'), api('/settings'), api('/dns').catch(() => demoDns), api('/status')]);
      state.devices = Array.isArray(devices) ? devices : (devices.devices || []); const everyDevice = allDevices ? (Array.isArray(allDevices) ? allDevices : (allDevices.devices || [])) : state.devices;
      state.nodes = (Array.isArray(nodes) ? nodes : (nodes.nodes || [])).map(n => ({ ...n, test_results: findNode(n.id)?.test_results })); state.settings = settings || {}; state.dns = Array.isArray(dns) ? dns : demoDns;
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
    const nodes = (data.nodes || []).filter(n => n.enabled !== false);
    const identified = !!(device?.ip && device?.mac) && data.can_bind !== false;
    $('#selfPreviewDevice').textContent = device?.hostname || '当前访问设备';
    $('#selfDeviceIP').textContent = device?.ip || data.ip || '未识别';
    $('#selfDeviceMAC').textContent = device?.mac || '未识别';
    $('#selfDeviceRemark').textContent = device?.remark || '暂无备注';
    $('#selfCurrentMode').textContent = device ? ({ proxy: '代理节点', direct: '本地直连', blocked: '禁止网络' }[device.mode] || '未设置') : '未识别';
    const boundNode = (data.nodes || []).find(n => n.id === device?.node_id);
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
    const title = { dashboard: '仪表盘', devices: '设备管理', nodes: '节点列表', dns: 'DNS 防泄露', settings: '设置', 'self-service': '设备自助页' }[page] || '仪表盘';
    $('#pageTitle').textContent = title;
    $('#sidebar')?.classList.remove('open');
    if (page === 'devices') renderDevices(); if (page === 'nodes') renderNodes();
    if (page === 'self-service' && !state.standalone) loadSelf();
  }

  function renderAll() { renderStatus(); renderDashboard(); renderDevices(); renderNodes(); renderSettings(); renderDns(); navigate(state.page); }
  function renderStatus() {
    $('#appVersion').textContent = `OpenPass v${String(state.status.version || '0.1.8').replace(/^v/, '')}`;
    const online = state.allDevices.filter(d => d.online && !d.hidden).length; const proxied = state.allDevices.filter(d => d.mode === 'proxy' && !d.hidden).length; const healthy = state.nodes.filter(n => nodeURLResult(n) === true && n.enabled !== false).length;
    $('#metricOnline').textContent = online; $('#metricProxied').textContent = proxied; $('#metricNodes').textContent = healthy; $('#onlineBadge').textContent = online; $('#nodesBadge').textContent = state.nodes.length; $('#onlineCount').textContent = online; $('#offlineCount').textContent = state.allDevices.filter(d => !d.online && !d.hidden).length; $('#hiddenCount').textContent = state.allDevices.filter(d => d.hidden).length; $('#hiddenTabCount').textContent = state.allDevices.filter(d => d.hidden).length;
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
    const nodes = state.nodes.slice(0, 4); $('#dashboardNodes').innerHTML = nodes.length ? nodes.map(n => { const latency = n.test_results?.url?.ok ? n.test_results.url.latency_ms : null; return `<div class="node-health"><span class="health-dot ${nodeURLResult(n) !== true ? 'off' : ''}"></span><span class="identity"><b>${esc(n.name || n.address)}</b><small>${esc((n.type || '').toUpperCase())} · ${esc(n.address || '')}</small></span><span class="latency">${latency ? `${latency} ms` : '待测试'}</span></div>`; }).join('') : '<div class="loading-row">暂无节点</div>';
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
  function renderNodes() {
    ensureNodeTestControls();
    const query = ($('#nodeSearch')?.value || '').toLowerCase();
    let rows = state.nodes.filter(n => [n.name, n.remark, n.type, n.address].some(value => String(value || '').toLowerCase().includes(query)));
    if (state.nodeFilter === 'healthy') rows = rows.filter(n => nodeURLResult(n) === true);
    if (state.nodeFilter === 'unhealthy') rows = rows.filter(n => nodeURLResult(n) === false);
    $('#nodeAvailable').textContent = state.nodes.filter(n => nodeURLResult(n) === true).length;
    $('#nodeChecking').textContent = state.nodes.filter(n => n.testing).length;
    $('#nodeUnavailable').textContent = state.nodes.filter(n => nodeURLResult(n) === false).length;
    const body = $('#nodesTable');
    if (!body) return;
    if (!rows.length) { body.innerHTML = '<tr><td colspan="6" class="empty-state">暂无符合条件的节点</td></tr>'; return; }
    body.innerHTML = rows.map(node => {
      const urlOK = nodeURLResult(node);
      const result = node.test_results?.url;
      const label = node.testing ? `${testLabels[node.testing]} 检测中` : urlOK === true ? 'URL 通过' : urlOK === false ? 'URL 失败' : '待 URL 测试';
      const statusClass = urlOK === false ? 'bad' : urlOK === true ? '' : 'off';
      const actions = ['ping', 'tcp', 'url'].map(type => `<button class="row-action test-action" data-action="test-node" data-test-type="${type}" data-id="${esc(node.id)}" title="${testLabels[type]} 测试" ${node.testing ? 'disabled' : ''}>${testLabels[type]}</button>`).join('');
      return `<tr><td><div class="device-cell"><span class="device-avatar">◈</span><span class="device-name">${esc(node.name || node.address)}${node.remark ? `<small class="node-remark">备注：${esc(node.remark)}</small>` : ''}<small>${esc(node.address || '')}:${esc(node.port || '')}</small></span></div></td><td><span class="protocol-badge ${esc(node.type || '')}">${esc(node.type || 'unknown')}</span></td><td><span class="latency">${result?.ok ? `${esc(result.latency_ms)} ms` : '—'}</span><small class="test-caption">代理 URL 延迟</small></td><td><span class="status-label ${statusClass}"><i></i>${label}</span><div class="node-test-results">${nodeTestSummary(node)}</div></td><td class="muted">${esc(node.last_test ? timeText(node.last_test) : '—')}</td><td class="align-right"><span class="row-actions"><button class="row-action" data-action="remark-node" data-id="${esc(node.id)}" title="编辑备注">备注</button><button class="row-action node-share" data-action="copy-node" data-id="${esc(node.id)}" title="复制节点链接">复制</button><button class="row-action node-share" data-action="export-node" data-id="${esc(node.id)}" title="导出节点链接">导出</button>${actions}<button class="row-action danger" data-action="delete-node" data-id="${esc(node.id)}" title="删除节点">×</button></span></td></tr>`;
    }).join('');
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
      const all = Array.isArray(payload) ? payload : (payload.devices || []);
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
    if (!node || node.testing) return;
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
    try { for (const node of state.nodes) await testNode(node.id, 'url'); }
    finally { button.disabled = false; }
  }
  async function saveSettings() { const body = { kill_switch: $('#killSwitch').checked, default_mode: $('#newDevicePolicy').value, self_service_enabled: $('#selfServiceEnabled').checked, hide_ap: $('#hideAp').checked }; try { if (!state.demo) { await api('/settings', { method: 'PUT', body }); await loadData(true); } else { state.settings = { ...state.settings, ...body }; renderStatus(); } toast('系统设置已保存'); } catch (error) { toast(`保存或刷新失败：${error.message}`, 'error'); } }
  async function saveDns() { const selected = $('input[name="defaultDns"]:checked')?.value || 'cloudflare'; const body = { default_dns: selected, custom_dns: $('#customDnsInput').value.trim(), force_doh: $('#forceDoh').checked, proxy_dns: $('#proxyDns').checked, dns_fail_closed: $('#dnsFailClosed').checked }; try { if (!state.demo) { await api('/settings', { method: 'PUT', body }); await loadData(true); } else { state.settings = { ...state.settings, ...body }; renderStatus(); } toast('DNS 防泄露设置已保存'); } catch (error) { toast(`保存或刷新失败：${error.message}`, 'error'); } }
  async function importNodes() {
    const subscription = !$('#importSubscriptionPane').classList.contains('hidden');
    const content = subscription ? '' : $('#nodeImportText').value.trim();
    const url = subscription ? $('#subscriptionUrl').value.trim() : '';
    if (!content && !url) { toast('请粘贴节点内容或订阅地址', 'error'); return; }
    let imported = null;
    try {
      const result = state.demo ? { imported: content.split(/\r?\n/).filter(Boolean).length || 3 } : await api('/nodes/import', { method: 'POST', body: { content, url } });
      imported = result.imported ?? result.nodes?.length ?? 0;
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
  function closeModal() { $('#modalBackdrop').classList.add('hidden'); }
  function setupEvents() {
    window.addEventListener('hashchange', () => navigate(location.hash.slice(1) || 'dashboard'));
    $$('.nav-item').forEach(el => el.addEventListener('click', e => { e.preventDefault(); navigate(el.dataset.page); }));
    $$('[data-goto]').forEach(el => el.addEventListener('click', () => navigate(el.dataset.goto)));
    $('#menuBtn')?.addEventListener('click', () => $('#sidebar').classList.toggle('open')); $('#refreshBtn')?.addEventListener('click', () => loadData()); $('#discoverBtn')?.addEventListener('click', refreshDevices); $('#importNodeBtn')?.addEventListener('click', openModal); $('#exportNodesBtn')?.addEventListener('click', () => exportNodes('uri')); $('#exportNodesJsonBtn')?.addEventListener('click', () => exportNodes('json')); $('#modalClose')?.addEventListener('click', closeModal); $('#modalCancel')?.addEventListener('click', closeModal); $('#modalBackdrop')?.addEventListener('click', e => { if (e.target.id === 'modalBackdrop') closeModal(); }); $('#importSubmit')?.addEventListener('click', importNodes);
    $$('.import-tab').forEach(tab => tab.addEventListener('click', () => { $$('.import-tab').forEach(t => t.classList.toggle('active', t === tab)); $('#importTextPane').classList.toggle('hidden', tab.dataset.importTab !== 'text'); $('#importSubscriptionPane').classList.toggle('hidden', tab.dataset.importTab !== 'subscription'); }));
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
    $$('[data-node-filter]').forEach(tab => tab.addEventListener('click', () => { $$('[data-node-filter]').forEach(t => t.classList.toggle('active', t === tab)); state.nodeFilter = tab.dataset.nodeFilter; renderNodes(); })); $('#nodeSearch')?.addEventListener('input', renderNodes); $('#nodesTable')?.addEventListener('click', e => { const el = e.target.closest('[data-action]'); if (!el || el.dataset.testType) return; if (el.dataset.action === 'test-node') testNode(el.dataset.id); if (el.dataset.action === 'remark-node') editNodeRemark(el.dataset.id); if (el.dataset.action === 'copy-node') copyNodeURI(el.dataset.id); if (el.dataset.action === 'export-node') exportNodeURI(el.dataset.id); if (el.dataset.action === 'delete-node' && confirm('确定删除这个节点吗？')) deleteNode(el.dataset.id); }); $('#testAllBtn')?.addEventListener('click', testAllNodes);
    $$('input[name="defaultDns"]').forEach(radio => radio.addEventListener('change', () => { $$('.dns-option').forEach(option => option.classList.toggle('selected', option.querySelector('input').checked)); $('#customDnsWrap').classList.toggle('hidden', radio.value !== 'custom' || !radio.checked); })); $('#saveDnsBtn')?.addEventListener('click', saveDns); $('#testDnsBtn')?.addEventListener('click', () => { $('#dnsLastTest').textContent = '尚未完成终端检测'; toast('请使用连接本路由器的设备进行 DNS 泄漏检测，配置状态不能替代实际检测。', 'error'); }); $('#saveSettingsBtn')?.addEventListener('click', saveSettings); $('#quickProtect')?.addEventListener('click', async () => { const enabled = !state.settings.enabled; try { if (!state.demo) { await api('/settings', { method: 'PUT', body: { enabled } }); await loadData(true); } else { state.settings.enabled = enabled; renderAll(); } toast(state.settings.enabled ? (state.status.kernel_running === true ? '全局保护已开启，内核运行中' : '全局保护已开启，但内核未运行，请检查配置') : '全局保护已关闭', state.settings.enabled && state.status.kernel_running !== true ? 'error' : 'success'); } catch (error) { toast(`操作或刷新失败：${error.message}`, 'error'); } }); $('#restartCoreBtn')?.addEventListener('click', async () => { try { if (!state.demo) { await api('/apply', { method: 'POST' }); await loadData(true); } toast(state.status.kernel_running === true ? 'sing-box 内核已重载' : '配置已应用，代理内核当前未运行', state.status.kernel_running === true ? 'success' : 'error'); } catch (error) { toast(`重启或刷新失败：${error.message}`, 'error'); } }); $('#openSelfServiceBtn')?.addEventListener('click', () => window.open('/choose', '_blank')); $('#copySelfUrl')?.addEventListener('click', () => navigator.clipboard?.writeText(selfURL()).then(() => toast('自助页地址已复制')));

  }
  async function deleteNode(id) { try { if (!state.demo) await api(`/nodes/${encodeURIComponent(id)}`, { method: 'DELETE' }); state.nodes = state.nodes.filter(n => String(n.id) !== String(id)); renderNodes(); renderStatus(); toast('节点已删除'); } catch (error) { toast(`删除失败：${error.message}`, 'error'); } }
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
  state.page = location.hash.slice(1) || 'dashboard';
  bootstrap();
})();
