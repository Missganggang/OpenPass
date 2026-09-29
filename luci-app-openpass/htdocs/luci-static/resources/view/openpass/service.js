'use strict';
'require view';
'require rpc';
'require poll';
'require ui';

var callStatus = rpc.declare({
	object: 'openpass',
	method: 'status',
	reject: true,
	nobatch: true
});

var callSetEnabled = rpc.declare({
	object: 'openpass',
	method: 'set_enabled',
	params: [ 'enabled' ],
	reject: true,
	nobatch: true
});

function validateStatus(result) {
	if (result && result.error)
		throw new Error(result.error);
	if (!result || typeof result.running !== 'boolean' ||
	    typeof result.enabled !== 'boolean' || typeof result.stopped !== 'boolean' ||
	    (result.cleanup_pending != null && typeof result.cleanup_pending !== 'boolean'))
		throw new Error(_('服务状态响应无效，请重试。'));
	return result;
}

function errorMessage(error) {
	return error && error.message ? error.message : String(error);
}

function needsClose(status) {
	return status && (status.running || status.enabled || status.cleanup_pending);
}

return view.extend({
	handleSaveApply: null,
	handleSave: null,
	handleReset: null,

	load: function() {
		// LuCI RPC uses this timeout in seconds; starting/stopping may take a while.
		L.env.rpctimeout = Math.max(45, Number(L.env.rpctimeout) || 0);
		return callStatus().then(validateStatus).then(function(status) {
			return { status: status };
		}).catch(function(error) {
			return { error: error };
		});
	},

	render: function(data) {
		// URL handles IPv4, DNS names, and bracketed IPv6 literals correctly.
		var endpoint = new URL(window.location.href);
		endpoint.protocol = 'http:';
		endpoint.port = '8787';
		endpoint.pathname = '/';
		endpoint.search = '';
		endpoint.hash = '';
		var manageURL = endpoint.href;
		endpoint.pathname = '/choose';
		var selfURL = endpoint.href;
		var currentStatus = data.status || null;
		var statusError = data.error || null;
		var busy = false;
		var refreshing = false;
		var statusText = E('strong', { 'role': 'status', 'aria-live': 'polite' });
		var statusDetail = E('div', { 'style': 'margin-top:.5em' });
		var toggleButton = E('button', {
			'class': 'cbi-button cbi-button-apply', 'type': 'button',
			'click': toggleService
		});
		var retryButton = E('button', {
			'class': 'cbi-button cbi-button-action', 'type': 'button',
			'click': refreshStatus
		}, _('重新读取状态'));
		var manageLink = E('a', {
			'class': 'cbi-button cbi-button-apply', 'target': '_blank',
			'rel': 'noopener noreferrer', 'click': guardLink
		}, _('打开管理页面'));
		var selfLink = E('a', {
			'class': 'cbi-button cbi-button-action', 'target': '_blank',
			'rel': 'noopener noreferrer', 'click': guardLink
		}, _('打开设备自助页'));

		function guardLink(event) {
			if (busy || !currentStatus || !currentStatus.running)
				event.preventDefault();
		}

		function updateLink(link, url, enabled) {
			if (enabled)
				link.setAttribute('href', url);
			else
				link.removeAttribute('href');
			link.setAttribute('aria-disabled', enabled ? 'false' : 'true');
			link.setAttribute('tabindex', enabled ? '0' : '-1');
			link.style.opacity = enabled ? '' : '.5';
			link.style.cursor = enabled ? '' : 'not-allowed';
		}

		function paintStatus() {
			var running = currentStatus && currentStatus.running;
			var pending = currentStatus && currentStatus.cleanup_pending;
			var canClose = needsClose(currentStatus);
			toggleButton.disabled = busy || refreshing || !currentStatus;
			toggleButton.textContent = busy ? _('正在切换，请稍候…') :
				!currentStatus ? _('等待服务状态') :
				pending ? _('重试关闭 OpenPass') :
				canClose ? _('关闭 OpenPass') : _('开启 OpenPass');
			toggleButton.className = 'cbi-button ' + (canClose ? 'cbi-button-negative' : 'cbi-button-apply');
			retryButton.style.display = statusError ? '' : 'none';
			retryButton.disabled = busy || refreshing;
			if (busy) {
				statusText.textContent = _('正在切换 OpenPass 服务状态…');
				statusDetail.textContent = _('请等待服务操作完成。');
			} else if (statusError || !currentStatus) {
				statusText.textContent = _('无法读取 OpenPass 状态');
				statusDetail.textContent = statusError ? errorMessage(statusError) : _('请重新读取状态。');
			} else if (pending) {
				statusText.textContent = _('OpenPass 关闭未完成');
				statusDetail.textContent = running ?
					_('关闭尚未完成，请重试关闭 OpenPass。') :
					_('后台已停止，但网络规则清理未完成，请重试关闭 OpenPass。');
			} else if (running) {
				statusText.textContent = _('OpenPass 已开启');
				statusDetail.textContent = currentStatus.enabled ?
					_('开机自动启动。关闭后会停止管理页面、自助页、代理和 DNS 接管，重启路由器后仍保持关闭。') :
					_('服务正在运行，当前未开启开机启动。关闭会停止管理页面、自助页、代理和 DNS 接管。');
			} else if (currentStatus.enabled) {
				statusText.textContent = _('OpenPass 服务异常');
				statusDetail.textContent = _('服务设为开机启动，但后台未运行。请先点击关闭彻底停用，再点击开启恢复服务。');
			} else {
				statusText.textContent = currentStatus.stopped ? _('OpenPass 已关闭') : _('OpenPass 服务未运行');
				statusDetail.textContent = currentStatus.stopped ?
					_('管理页面、自助页、代理和 DNS 接管均已停止，重启后保持关闭。开启后恢复原有配置。') :
					_('管理页面和自助页当前不可用。点击开启可启动服务并恢复原有配置。');
			}
			updateLink(manageLink, manageURL, !!running && !busy);
			updateLink(selfLink, selfURL, !!running && !busy);
		}

		function refreshStatus() {
			if (busy || refreshing)
				return Promise.resolve();
			refreshing = true;
			paintStatus();
			return callStatus().then(validateStatus).then(function(status) {
				currentStatus = status;
				statusError = null;
			}).catch(function(error) {
				currentStatus = null;
				statusError = error;
			}).then(function() {
				refreshing = false;
				paintStatus();
			});
		}

		function toggleService() {
			if (busy || refreshing || !currentStatus)
				return Promise.resolve();
			var enable = !needsClose(currentStatus);
			busy = true;
			paintStatus();
			return callSetEnabled(enable).then(validateStatus).then(function(status) {
				currentStatus = status;
				statusError = null;
				if (enable ? (!status.running || !status.enabled || status.cleanup_pending) : needsClose(status))
					throw new Error(_('服务状态尚未切换，请稍后重试。'));
				ui.addNotification(null, E('p', {}, enable ?
					_('OpenPass 已开启，已恢复原有配置。') :
					_('OpenPass 已关闭，重启路由器后仍保持关闭。')), 'info');
			}).catch(function(error) {
				currentStatus = null;
				statusError = error;
				ui.addNotification(null, E('p', {}, _('操作失败：') + errorMessage(error)), 'error');
			}).then(function() {
				busy = false;
				paintStatus();
				return refreshStatus();
			});
		}

		paintStatus();
		poll.add(refreshStatus, 5);

		return E('div', { 'class': 'cbi-map' }, [
			E('h2', {}, _('OpenPass')),
			E('div', { 'class': 'cbi-map-descr' }, _('控制 OpenPass 服务，或打开管理页面和设备自助页。LuCI 入口在服务关闭后仍可使用。')),
			E('div', { 'class': 'cbi-section', 'style': 'padding:1.5em' }, [
				statusText,
				statusDetail,
				E('div', { 'style': 'display:flex;flex-wrap:wrap;gap:1em;margin-top:1em' }, [toggleButton, retryButton])
			]),
			E('div', { 'class': 'cbi-section', 'style': 'display:flex;flex-wrap:wrap;gap:1em;padding:1.5em' }, [
				manageLink,
				selfLink
			])
		]);
	}
});
