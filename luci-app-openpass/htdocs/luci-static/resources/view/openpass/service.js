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

var callCheckUpdate = rpc.declare({ object: 'openpass', method: 'check_update', reject: true, nobatch: true });
var callUpdate = rpc.declare({ object: 'openpass', method: 'update', params: [ 'version' ], reject: true, nobatch: true });
var callMaintenanceStatus = rpc.declare({ object: 'openpass', method: 'maintenance_status', reject: true, nobatch: true });
var callUninstall = rpc.declare({ object: 'openpass', method: 'uninstall', params: [ 'keep_config', 'confirm' ], reject: true, nobatch: true });

function validateResult(result) {
	if (result && result.error)
		throw new Error(result.error);
	if (!result || typeof result !== 'object')
		throw new Error(_('服务器响应无效，请重试。'));
	return result;
}

function versionLabel(version) {
	return version ? 'v' + String(version).replace(/^v/, '') : _('未知');
}

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
		L.env.rpctimeout = Math.max(90, Number(L.env.rpctimeout) || 0);
		return Promise.all([
			callStatus().then(validateStatus).then(function(status) { return { status: status }; }).catch(function(error) { return { error: error }; }),
			callMaintenanceStatus().then(validateResult).catch(function() { return null; })
		]).then(function(results) {
			results[0].maintenance = results[1];
			return results[0];
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
		var pendingUpdate = data.maintenance && data.maintenance.action === 'update' && data.maintenance.state === 'running';
		var busy = !!pendingUpdate;
		var busyAction = pendingUpdate ? 'update' : '';
		var refreshing = false;
		var dialogOpen = false;
		var uninstalled = false;
		var maintenance = pendingUpdate ? { version: String(data.maintenance.version || '').replace(/^v/, ''), started: Date.now(), paused: false, message: data.maintenance.message } : null;
		var versionText = E('span', { 'class': 'openpass-version' });
		var statusText = E('strong', { 'role': 'status', 'aria-live': 'polite' });
		var statusDetail = E('div', { 'style': 'margin-top:.5em' });
		var overviewLink = E('a', { 'href': L.url('admin/status/overview'), 'class': 'cbi-button cbi-button-action', 'style': 'display:none;margin-top:1em' }, _('返回 LuCI 概览'));
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
		var updateButton = E('button', {
			'class': 'cbi-button cbi-button-action', 'type': 'button', 'click': checkUpdate
		}, _('检查更新'));
		var uninstallButton = E('button', {
			'class': 'cbi-button cbi-button-negative', 'type': 'button', 'click': confirmUninstall
		}, _('卸载 OpenPass'));

		function guardLink(event) {
			if (busy || uninstalled || !currentStatus || !currentStatus.running)
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
			versionText.textContent = _('当前版本：') + versionLabel(currentStatus && currentStatus.version);
			toggleButton.disabled = busy || refreshing || !currentStatus || uninstalled;
			updateButton.disabled = busy || refreshing || !currentStatus || uninstalled;
			uninstallButton.disabled = busy || refreshing || !currentStatus || uninstalled;
			updateButton.textContent = busyAction === 'check' ? _('正在检查更新…') : busyAction === 'update' ? _('正在更新…') : _('检查更新');
			toggleButton.textContent = busyAction === 'toggle' ? _('正在切换，请稍候…') :
				!currentStatus ? _('等待服务状态') :
				pending ? _('重试关闭 OpenPass') :
				canClose ? _('关闭 OpenPass') : _('开启 OpenPass');
			toggleButton.className = 'cbi-button ' + (canClose ? 'cbi-button-negative' : 'cbi-button-apply');
			retryButton.style.display = !uninstalled && (statusError || (maintenance && maintenance.paused)) ? '' : 'none';
			retryButton.disabled = refreshing || (busy && !(maintenance && maintenance.paused));
			if (uninstalled) {
				statusText.textContent = _('OpenPass 已卸载');
				statusDetail.textContent = _('代理服务和 LuCI 入口已移除。可返回 LuCI 概览。');
				overviewLink.style.display = '';
			} else if (maintenance) {
				statusText.textContent = maintenance.paused ? _('更新状态待确认') : _('正在更新 OpenPass…');
				statusDetail.textContent = maintenance.message || _('正在安装新版本并保留原有配置，请勿关闭路由器电源。');
			} else if (busyAction === 'uninstall') {
				statusText.textContent = _('正在卸载 OpenPass…');
				statusDetail.textContent = _('正在停止代理服务并清理安装文件，请稍候。');
			} else if (busyAction === 'toggle') {
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
			updateLink(manageLink, manageURL, !!running && !busy && !uninstalled);
			updateLink(selfLink, selfURL, !!running && !busy && !uninstalled);
		}

		function refreshStatus() {
			if (uninstalled)
				return Promise.resolve();
			if (maintenance) {
				if (maintenance.paused) {
					maintenance.paused = false;
					maintenance.started = Date.now();
				}
				return refreshMaintenance();
			}
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
			if (busy || refreshing || !currentStatus || uninstalled)
				return Promise.resolve();
			var enable = !needsClose(currentStatus);
			busy = true;
			busyAction = 'toggle';
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
				busyAction = '';
				paintStatus();
				return refreshStatus();
			});
		}

		function checkUpdate() {
			if (busy || refreshing || !currentStatus || uninstalled)
				return Promise.resolve();
			busy = true;
			busyAction = 'check';
			paintStatus();
			return callCheckUpdate().then(validateResult).then(function(result) {
				if (!result.latest_version || typeof result.update_available !== 'boolean')
					throw new Error(_('版本信息不完整，请重试。'));
				if (result.current_version)
					currentStatus.version = result.current_version;
				var buttons = [ E('button', { 'class': 'cbi-button', 'type': 'button', 'click': closeDialog }, _('关闭')) ];
				if (result.update_available)
					buttons.push(E('button', { 'class': 'cbi-button cbi-button-apply', 'type': 'button', 'click': function() { closeDialog(); return startUpdate(result.latest_version); } }, _('立即更新')));
				var content = [
					E('p', {}, _('当前版本：') + versionLabel(result.current_version || currentStatus.version)),
					E('p', {}, _('最新版本：') + versionLabel(result.latest_version)),
					E('p', {}, result.update_available ? _('更新会短暂停止代理服务，保留节点、设备绑定及其他配置。关闭状态的服务更新后仍保持关闭。') : _('当前已是最新版本。'))
				];
				if (result.release_url && /^https:\/\/github\.com\/Missganggang\/OpenPass\/releases\//.test(result.release_url))
					content.push(E('p', {}, E('a', { 'href': result.release_url, 'target': '_blank', 'rel': 'noopener noreferrer' }, _('查看版本说明'))));
				content.push(E('div', { 'class': 'right', 'style': 'display:flex;justify-content:flex-end;gap:.5em' }, buttons));
				dialogOpen = true;
				ui.showModal(result.update_available ? _('发现 OpenPass 新版本') : _('检查更新'), content);
			}).catch(function(error) {
				ui.addNotification(null, E('p', {}, _('检查更新失败：') + errorMessage(error)), 'error');
			}).then(function() {
				busy = false;
				busyAction = '';
				paintStatus();
			});
		}

		function startUpdate(version) {
			if (busy || refreshing || !currentStatus || uninstalled)
				return Promise.resolve();
			busy = true;
			busyAction = 'update';
			maintenance = { version: String(version).replace(/^v/, ''), started: Date.now(), paused: false, submitting: true };
			paintStatus();
			return callUpdate('v' + maintenance.version).then(function(result) {
				try {
					validateResult(result);
					if (result.accepted !== true)
						throw new Error(_('更新任务未被接受，请重试。'));
				} catch (error) {
					finishUpdate(error);
					return;
				}
				maintenance.submitting = false;
				return refreshMaintenance();
			}, function(error) {
				// An RPC restart can lose the acceptance response; only the job status is conclusive.
				maintenance.submitting = false;
				maintenance.message = _('暂时无法确认更新请求，正在重新读取任务状态：') + errorMessage(error);
				paintStatus();
				return refreshMaintenance();
			});
		}

		function finishUpdate(error, result) {
			if (error)
				ui.addNotification(null, E('p', {}, _('更新失败：') + errorMessage(error)), 'error');
			else {
				if (currentStatus)
					currentStatus.version = result.version;
				ui.addNotification(null, E('p', {}, _('OpenPass 已更新到 ') + versionLabel(result.version) + _('，原有配置已保留。')), 'info');
			}
			maintenance = null;
			busy = false;
			busyAction = '';
			paintStatus();
		}

		function refreshMaintenance() {
			if (!maintenance || maintenance.paused || maintenance.submitting || refreshing || uninstalled)
				return Promise.resolve();
			refreshing = true;
			return callMaintenanceStatus().then(validateResult).then(function(result) {
				if (result.action === 'update' && result.state === 'running')
					maintenance.message = result.message || _('正在安装新版本，请稍候。');
				else if (result.action === 'update' && result.state === 'success' && String(result.version).replace(/^v/, '') === maintenance.version)
					finishUpdate(null, result);
				else if (result.action === 'update' && result.state === 'error')
					finishUpdate(new Error(result.message || _('更新任务失败。')));
				else
					maintenance.message = _('等待更新任务确认，请稍候。');
			}).catch(function(error) {
				if (maintenance)
					maintenance.message = _('更新期间服务可能短暂重启，正在等待恢复：') + errorMessage(error);
			}).then(function() {
				refreshing = false;
				if (maintenance && Date.now() - maintenance.started > 10 * 60 * 1000) {
					maintenance.paused = true;
					maintenance.message = _('暂未确认更新结果。请点击“重新读取状态”继续检查，或稍后重新打开此页面。');
				}
				paintStatus();
				if (!maintenance)
					return refreshStatus();
			});
		}

		function confirmUninstall() {
			if (busy || refreshing || !currentStatus || uninstalled)
				return;
			var keepConfig = E('input', { 'type': 'checkbox', 'checked': 'checked' });
			dialogOpen = true;
			ui.showModal(_('卸载 OpenPass'), [
				E('p', {}, _('卸载会停止 OpenPass 代理和 DNS 接管，移除管理页面、自助页及 LuCI 入口。设备将恢复路由器原有网络策略。')),
				E('label', { 'style': 'display:flex;align-items:center;gap:.5em;margin:1em 0' }, [keepConfig, _('保留节点、设备绑定及其他配置，方便以后重新安装')]),
				E('p', {}, _('取消勾选后，OpenPass 配置也会被删除，无法恢复。')),
				E('div', { 'class': 'right', 'style': 'display:flex;justify-content:flex-end;gap:.5em' }, [
					E('button', { 'class': 'cbi-button', 'type': 'button', 'click': closeDialog }, _('取消')),
					E('button', { 'class': 'cbi-button cbi-button-negative', 'type': 'button', 'click': function() { closeDialog(); return uninstall(keepConfig.checked); } }, _('确认卸载'))
				])
			]);
		}

		function closeDialog() {
			dialogOpen = false;
			ui.hideModal();
		}

		function uninstall(keepConfig) {
			if (busy || refreshing || !currentStatus || uninstalled)
				return Promise.resolve();
			busy = true;
			busyAction = 'uninstall';
			paintStatus();
			return callUninstall(!!keepConfig, true).then(validateResult).then(function(result) {
				if (result.uninstalled !== true)
					throw new Error(_('未能确认卸载完成，请重新读取状态。'));
				uninstalled = true;
				poll.remove(pollStatus);
				ui.addNotification(null, E('p', {}, result.config_kept ? _('OpenPass 已卸载，原有配置已保留。') : _('OpenPass 已卸载，配置已删除。')), 'info');
			}).catch(function(error) {
				ui.addNotification(null, E('p', {}, _('卸载失败或结果未确认：') + errorMessage(error)), 'error');
				currentStatus = null;
				statusError = error;
			}).then(function() {
				busy = false;
				busyAction = '';
				paintStatus();
			});
		}

		function pollStatus() {
			if (dialogOpen)
				return Promise.resolve();
			if (maintenance)
				return refreshMaintenance();
			return refreshStatus();
		}

		paintStatus();
		poll.add(pollStatus, 5);

		return E('div', { 'class': 'cbi-map' }, [
			E('h2', {}, _('OpenPass')),
			E('p', {}, versionText),
			E('div', { 'class': 'cbi-map-descr' }, _('控制 OpenPass 服务，或打开管理页面和设备自助页。LuCI 入口在服务关闭后仍可使用。')),
			E('div', { 'class': 'cbi-section', 'style': 'padding:1.5em' }, [
				statusText,
				statusDetail,
				overviewLink,
				E('div', { 'style': 'display:flex;flex-wrap:wrap;gap:1em;margin-top:1em' }, [toggleButton, retryButton])
			]),
			E('div', { 'class': 'cbi-section', 'style': 'display:flex;flex-wrap:wrap;gap:1em;padding:1.5em' }, [
				manageLink,
				selfLink,
				updateButton,
				uninstallButton
			])
		]);
	}
});
