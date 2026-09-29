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
    self: null, selfMode: null, selfLoading: false,
    demo: false
  };

  const demoDns = [{ id: 'cloudflare', name: 'Cloudflare', url: 'https://cloudflare-dns.com/dns-query' }, { id: 'aliyun', name: '阿里 DoH', url: 'https://dns.alidns.com/dns-query' }, { id: 'tencent', name: '腾讯 DoH', url: 'https://doh.pub/dns-query' }];

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

  async function loadData() {
    try {
      const [devices, allDevices, nodes, settings, dns] = await Promise.all([api('/devices'), api('/devices?hidden=all').catch(() => null), api('/nodes'), api('/settings'), api('/dns').catch(() => demoDns)]);
      state.devices = Array.isArray(devices) ? devices : (devices.devices || []); const everyDevice = allDevices ? (Array.isArray(allDevices) ? allDevices : (allDevices.devices || [])) : state.devices;
      state.nodes = (Array.isArray(nodes) ? nodes : (nodes.nodes || [])).map(n => ({ ...n, test_results: findNode(n.id)?.test_results })); state.settings = settings || {}; state.dns = Array.isArray(dns) ? dns : demoDns;
      state.allDevices = everyDevice;
      if (state.settings.enabled != null) state.status.enabled = state.settings.enabled;
    } catch (error) {
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
    $('#selfCurrentMode').textContent = device ? ({ proxy: '代理节点', direct: '本地直连', blocked: '禁止网络' }[device.mode] || '未设置') : '未识别';
    $('#selfCurrentNode').textContent = device?.mode === 'proxy' && device.node_id ? ((data.nodes || []).find(n => n.id === device.node_id)?.name || '节点已删除或停用') : '未绑定';
    const enabled = data.enabled ?? data.protection_enabled ?? state.settings.enabled;
    $('#selfServiceNotice').textContent = enabled === false ? '全局保护当前关闭。选择会被保存，管理员开启保护后生效。' : enabled === true ? '保存后将更新当前设备的网络方式。' : '请先确认设备信息，再选择网络方式。';
    $('#selfServiceNotice').classList.toggle('notice-warning', enabled === false);
    $('#selfNodeSelect').innerHTML = '<option value="">请选择代理节点</option>' + nodes.map(n => `<option value="${esc(n.id)}">${esc(n.name || n.type || '代理节点')}</option>`).join('');
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
    const online = state.allDevices.filter(d => d.online && !d.hidden).length; const proxied = state.allDevices.filter(d => d.mode === 'proxy' && !d.hidden).length; const healthy = state.nodes.filter(n => nodeURLResult(n) === true && n.enabled !== false).length;
    $('#metricOnline').textContent = online; $('#metricProxied').textContent = proxied; $('#metricNodes').textContent = healthy; $('#onlineBadge').textContent = online; $('#nodesBadge').textContent = state.nodes.length; $('#onlineCount').textContent = online; $('#offlineCount').textContent = state.allDevices.filter(d => !d.online && !d.hidden).length; $('#hiddenCount').textContent = state.allDevices.filter(d => d.hidden).length; $('#hiddenTabCount').textContent = state.allDevices.filter(d => d.hidden).length;
    $('#metricDns').textContent = state.settings.default_dns ? dnsName(state.settings.default_dns) : 'DoH 安全'; $('#metricDnsSub').textContent = state.settings.force_doh === false ? '加密解析未强制' : 'DoH 加密解析'; $('#kernelVersion').textContent = state.status.kernel || state.status.version || 'sing-box 运行中'; $('#routerAddress').textContent = state.status.router || 'OpenWrt · 10.0.0.1';
    const protect = $('#quickProtect'); if (protect) { const on = !!state.settings.enabled || !!state.status.enabled; protect.innerHTML = on ? '✓ 全局保护已开启' : '◉ 开启全局保护'; protect.classList.toggle('btn-ghost', on); protect.classList.toggle('btn-primary', !on); }
  }
  function renderDashboard() {
    const visible = state.allDevices.filter(d => d.online && !d.hidden).slice(0, 4); $('#dashboardDevices').innerHTML = visible.length ? visible.map(d => `<div class="mini-device"><span class="device-avatar">◉</span><span class="identity"><b>${esc(d.hostname || '未命名设备')}</b><small>${esc(d.ip)} · ${esc(d.mac)}</small></span><span class="device-mode">${d.mode === 'proxy' ? esc(nodeName(d.node_id)) : d.mode === 'direct' ? '直连网络' : '已阻断'}</span></div>`).join('') : '<div class="loading-row">暂无在线设备</div>';
    const nodes = state.nodes.slice(0, 4); $('#dashboardNodes').innerHTML = nodes.length ? nodes.map(n => { const latency = n.test_results?.url?.ok ? n.test_results.url.latency_ms : null; return `<div class="node-health"><span class="health-dot ${nodeURLResult(n) !== true ? 'off' : ''}"></span><span class="identity"><b>${esc(n.name || n.address)}</b><small>${esc((n.type || '').toUpperCase())} · ${esc(n.address || '')}</small></span><span class="latency">${latency ? `${latency} ms` : '待测试'}</span></div>`; }).join('') : '<div class="loading-row">暂无节点</div>';
  }
  function deviceModeSelect(d) { return `<select class="mode-select ${esc(d.mode || '')}" data-action="mode" data-id="${esc(d.id)}"><option value="proxy" ${d.mode === 'proxy' ? 'selected' : ''}>代理节点</option><option value="direct" ${d.mode === 'direct' ? 'selected' : ''}>直连网络</option><option value="blocked" ${d.mode === 'blocked' ? 'selected' : ''}>禁止网络</option></select>${d.mode === 'proxy' ? `<select class="mode-select proxy node-select" data-action="node" data-id="${esc(d.id)}"><option value="">选择节点</option>${state.nodes.map(n => `<option value="${esc(n.id)}" ${String(d.node_id) === String(n.id) ? 'selected' : ''}>${esc(n.name || n.address)}</option>`).join('')}</select>` : ''}`; }
  function dnsSelect(d) { const all = state.dns.length ? state.dns.slice() : demoDns.slice(); if (state.settings.custom_dns && !all.some(x => x.id === 'custom')) all.push({ id: 'custom', name: '自定义 DoH' }); return `<select class="mode-select" data-action="dns" data-id="${esc(d.id)}">${all.map(x => `<option value="${esc(x.id)}" ${String(d.dns || state.settings.default_dns) === String(x.id) ? 'selected' : ''}>${esc(x.name)}</option>`).join('')}</select>`; }
  function renderDevices() {
    const query = ($('#deviceSearch')?.value || '').toLowerCase(); const source = state.deviceTab === 'hidden' ? state.allDevices.filter(d => d.hidden) : state.allDevices.filter(d => !d.hidden && (state.deviceTab === 'online' ? d.online : !d.online)); const rows = source.filter(d => [d.hostname, d.ip, d.mac].some(v => String(v || '').toLowerCase().includes(query)));
    const body = $('#devicesTable'); if (!body) return; if (!rows.length) { body.innerHTML = `<tr><td colspan="6" class="empty-state">${state.deviceTab === 'hidden' ? '暂无隐藏设备' : '暂无符合条件的设备'}</td></tr>`; return; }
    body.innerHTML = rows.map(d => `<tr><td><div class="device-cell"><span class="device-avatar">${d.hidden ? '⌁' : '◉'}</span><span class="device-name">${esc(d.hostname || '未命名设备')}<small><span class="status-label ${d.online ? '' : 'off'}"><i></i>${d.online ? '在线' : '离线'}</span></small></span></div></td><td class="mono">${esc(d.ip || '—')}</td><td class="mono">${esc(d.mac || '—')}</td><td>${d.hidden ? '<span class="muted">已隐藏</span>' : deviceModeSelect(d)}</td><td>${dnsSelect(d)}</td><td class="align-right"><span class="row-actions"><button class="row-action" data-action="hide" data-id="${esc(d.id)}" title="${d.hidden ? '取消隐藏' : '隐藏设备'}">${d.hidden ? '⊙' : '◌'}</button><button class="row-action danger" data-action="block" data-id="${esc(d.id)}" title="阻断设备">⊘</button></span></td></tr>`).join('');
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
      return `<tr><td><div class="device-cell"><span class="device-avatar">◈</span><span class="device-name">${esc(node.name || node.address)}${node.remark ? `<small class="node-remark">备注：${esc(node.remark)}</small>` : ''}<small>${esc(node.address || '')}:${esc(node.port || '')}</small></span></div></td><td><span class="protocol-badge ${esc(node.type || '')}">${esc(node.type || 'unknown')}</span></td><td><span class="latency">${result?.ok ? `${esc(result.latency_ms)} ms` : '—'}</span><small class="test-caption">代理 URL 延迟</small></td><td><span class="status-label ${statusClass}"><i></i>${label}</span><div class="node-test-results">${nodeTestSummary(node)}</div></td><td class="muted">${esc(node.last_test ? timeText(node.last_test) : '—')}</td><td class="align-right"><span class="row-actions"><button class="row-action" data-action="remark-node" data-id="${esc(node.id)}" title="编辑备注">备注</button>${actions}<button class="row-action danger" data-action="delete-node" data-id="${esc(node.id)}" title="删除节点">×</button></span></td></tr>`;
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
  function renderSettings() { const s = state.settings; $('#killSwitch').checked = s.kill_switch !== false; $('#newDevicePolicy').value = s.default_mode || 'direct'; $('#selfServiceEnabled').checked = s.self_service_enabled !== false; $('#hideAp').checked = s.hide_ap !== false; $('#serviceUptime').textContent = state.status.uptime ? `运行 ${state.status.uptime}` : '服务在线'; if ($('#selfServiceUrl')) $('#selfServiceUrl').textContent = selfURL(); if ($('.self-url span')) $('.self-url span').textContent = selfURL(); }

  async function patchDevice(id, body) { try { if (!state.demo) await api(`/devices/${encodeURIComponent(id)}`, { method: 'PATCH', body }); const d = state.allDevices.find(x => String(x.id) === String(id)); if (d) Object.assign(d, body); renderStatus(); renderDevices(); toast('设备设置已保存'); } catch (error) { toast(`保存失败：${error.message}`, 'error'); } }
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
  async function saveSettings() { const body = { ...state.settings, enabled: !!$('#quickProtect')?.dataset.forceEnabled || !!state.settings.enabled, kill_switch: $('#killSwitch').checked, default_mode: $('#newDevicePolicy').value, self_service_enabled: $('#selfServiceEnabled').checked, hide_ap: $('#hideAp').checked }; delete body.forceEnabled; try { if (!state.demo) await api('/settings', { method: 'PUT', body }); state.settings = { ...state.settings, ...body }; renderStatus(); toast('系统设置已保存'); } catch (error) { toast(`保存失败：${error.message}`, 'error'); } }
  async function saveDns() { const selected = $('input[name="defaultDns"]:checked')?.value || 'cloudflare'; const body = { default_dns: selected, custom_dns: $('#customDnsInput').value.trim(), force_doh: $('#forceDoh').checked, proxy_dns: $('#proxyDns').checked, dns_fail_closed: $('#dnsFailClosed').checked }; try { if (!state.demo) await api('/settings', { method: 'PUT', body }); state.settings = { ...state.settings, ...body }; renderStatus(); toast('DNS 防泄露设置已保存'); } catch (error) { toast(`保存失败：${error.message}`, 'error'); } }
  async function importNodes() { const subscription = !$('#importSubscriptionPane').classList.contains('hidden'); const content = subscription ? '' : $('#nodeImportText').value.trim(); const url = subscription ? $('#subscriptionUrl').value.trim() : ''; if (!content && !url) { toast('请粘贴节点内容或订阅地址', 'error'); return; } try { const result = state.demo ? { imported: content.split(/\r?\n/).filter(Boolean).length || 3 } : await api('/nodes/import', { method: 'POST', body: { content, url } }); if (result.nodes) state.nodes = result.nodes; const imported = result.imported ?? result.nodes?.length ?? 0; toast(`成功导入 ${imported} 个节点`); closeModal(); await loadData(); if (imported > 0 && !state.settings.enabled) { state.settings.enabled = true; if (!state.demo) await api('/settings', { method: 'PUT', body: { enabled: true } }); toast('已自动开启全局保护'); } if (!state.demo) api('/apply', { method: 'POST' }).catch(() => {}); renderStatus(); } catch (error) { toast(`导入失败：${error.message}`, 'error'); } }

  function exportNodes(format) {
    if (state.demo) { toast('演示模式不提供导出', 'error'); return; }
    const link = document.createElement('a'); link.href = `${API}/nodes/export?format=${encodeURIComponent(format || 'uri')}`; link.download = format === 'json' ? 'openpass-nodes.json' : 'openpass-nodes.txt'; document.body.appendChild(link); link.click(); link.remove(); toast('节点导出已开始');
  }

  async function editNodeRemark(id) {
    const node = findNode(id); if (!node) return;
    const value = window.prompt('请输入节点备注（留空可清除）', node.remark || '');
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
    $('#menuBtn')?.addEventListener('click', () => $('#sidebar').classList.toggle('open')); $('#refreshBtn')?.addEventListener('click', () => loadData()); $('#importNodeBtn')?.addEventListener('click', openModal); $('#exportNodesBtn')?.addEventListener('click', () => exportNodes('uri')); $('#exportNodesJsonBtn')?.addEventListener('click', () => exportNodes('json')); $('#modalClose')?.addEventListener('click', closeModal); $('#modalCancel')?.addEventListener('click', closeModal); $('#modalBackdrop')?.addEventListener('click', e => { if (e.target.id === 'modalBackdrop') closeModal(); }); $('#importSubmit')?.addEventListener('click', importNodes);
    $$('.import-tab').forEach(tab => tab.addEventListener('click', () => { $$('.import-tab').forEach(t => t.classList.toggle('active', t === tab)); $('#importTextPane').classList.toggle('hidden', tab.dataset.importTab !== 'text'); $('#importSubscriptionPane').classList.toggle('hidden', tab.dataset.importTab !== 'subscription'); }));
    $$('[data-device-tab]').forEach(tab => tab.addEventListener('click', () => { $$('[data-device-tab]').forEach(t => t.classList.toggle('active', t === tab)); state.deviceTab = tab.dataset.deviceTab; renderDevices(); })); $('#deviceSearch')?.addEventListener('input', renderDevices); $('#showHiddenBtn')?.addEventListener('click', () => { state.deviceTab = 'hidden'; $$('[data-device-tab]').forEach(t => t.classList.toggle('active', t.dataset.deviceTab === 'hidden')); renderDevices(); });
    $('#devicesTable')?.addEventListener('change', e => { const el = e.target; const id = el.dataset.id; if (!id) return; if (el.dataset.action === 'mode') patchDevice(id, { mode: el.value, node_id: el.value === 'proxy' ? (state.allDevices.find(d => String(d.id) === String(id))?.node_id || null) : null }); if (el.dataset.action === 'node') patchDevice(id, { node_id: el.value, mode: 'proxy' }); if (el.dataset.action === 'dns') patchDevice(id, { dns: el.value }); });
    $('#devicesTable')?.addEventListener('click', e => { const el = e.target.closest('[data-action]'); if (!el) return; if (el.dataset.action === 'hide') patchDevice(el.dataset.id, { hidden: !state.allDevices.find(d => String(d.id) === String(el.dataset.id))?.hidden }); if (el.dataset.action === 'block') patchDevice(el.dataset.id, { mode: 'blocked', node_id: null }); });
    $$('[data-node-filter]').forEach(tab => tab.addEventListener('click', () => { $$('[data-node-filter]').forEach(t => t.classList.toggle('active', t === tab)); state.nodeFilter = tab.dataset.nodeFilter; renderNodes(); })); $('#nodeSearch')?.addEventListener('input', renderNodes); $('#nodesTable')?.addEventListener('click', e => { const el = e.target.closest('[data-action]'); if (!el || el.dataset.testType) return; if (el.dataset.action === 'test-node') testNode(el.dataset.id); if (el.dataset.action === 'remark-node') editNodeRemark(el.dataset.id); if (el.dataset.action === 'delete-node' && confirm('确定删除这个节点吗？')) deleteNode(el.dataset.id); }); $('#testAllBtn')?.addEventListener('click', testAllNodes);
    $$('input[name="defaultDns"]').forEach(radio => radio.addEventListener('change', () => { $$('.dns-option').forEach(option => option.classList.toggle('selected', option.querySelector('input').checked)); $('#customDnsWrap').classList.toggle('hidden', radio.value !== 'custom' || !radio.checked); })); $('#saveDnsBtn')?.addEventListener('click', saveDns); $('#testDnsBtn')?.addEventListener('click', () => { $('#dnsLastTest').textContent = '尚未完成终端检测'; toast('请使用连接本路由器的设备进行 DNS 泄漏检测，配置状态不能替代实际检测。', 'error'); }); $('#saveSettingsBtn')?.addEventListener('click', saveSettings); $('#quickProtect')?.addEventListener('click', async () => { state.settings.enabled = !state.settings.enabled; try { if (!state.demo) await api('/settings', { method: 'PUT', body: { enabled: state.settings.enabled } }); renderStatus(); toast(state.settings.enabled ? '全局保护已开启' : '全局保护已关闭'); } catch (error) { state.settings.enabled = !state.settings.enabled; toast(`操作失败：${error.message}`, 'error'); } }); $('#restartCoreBtn')?.addEventListener('click', async () => { try { if (!state.demo) await api('/apply', { method: 'POST' }); toast('sing-box 内核已重载'); } catch (error) { toast(`重启失败：${error.message}`, 'error'); } }); $('#openSelfServiceBtn')?.addEventListener('click', () => window.open('/choose', '_blank')); $('#copySelfUrl')?.addEventListener('click', () => navigator.clipboard?.writeText(selfURL()).then(() => toast('自助页地址已复制')));

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
        toast('连接方式已保存');
      } catch (error) {
        $('#selfError').textContent = `保存失败：${error.message}`;
        $('#selfError').classList.remove('hidden');
        $('#selfConfirmBtn').disabled = false;
        toast(`保存失败：${error.message}`, 'error');
      }
    });
  }
  function setupNodeTestTypeEvents() {
    $('#nodesTable')?.addEventListener('click', e => { const button = e.target.closest('[data-test-type]'); if (button) testNode(button.dataset.id, button.dataset.testType); });
  }

  setupEvents();
  setupSelfEvents();
  setupNodeTestTypeEvents();
  state.page = location.hash.slice(1) || 'dashboard';
  bootstrap();
})();
