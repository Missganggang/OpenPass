'use strict';
'require view';

return view.extend({
	handleSaveApply: null,
	handleSave: null,
	handleReset: null,

	render: function() {
		// URL handles IPv4, DNS names, and bracketed IPv6 literals correctly.
		var endpoint = new URL(window.location.href);
		endpoint.protocol = 'http:';
		endpoint.port = '8787';
		endpoint.pathname = '/';
		endpoint.search = '';
		endpoint.hash = '';
		var manageURL = endpoint.href;
		endpoint.pathname = '/choose';

		return E('div', { 'class': 'cbi-map' }, [
			E('h2', {}, _('OpenPass')),
			E('div', { 'class': 'cbi-map-descr' }, _('打开 OpenPass 页面，管理网络或选择当前设备的代理节点。')),
			E('div', { 'class': 'cbi-section', 'style': 'display:flex;flex-wrap:wrap;gap:1em;padding:1.5em' }, [
				E('a', { 'class': 'cbi-button cbi-button-apply', 'href': manageURL, 'target': '_blank', 'rel': 'noopener noreferrer' }, _('打开管理页面')),
				E('a', { 'class': 'cbi-button cbi-button-action', 'href': endpoint.href, 'target': '_blank', 'rel': 'noopener noreferrer' }, _('打开设备自助页'))
			])
		]);
	}
});
