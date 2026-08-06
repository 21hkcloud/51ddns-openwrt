'use strict';
'require view';
'require form';
'require rpc';
'require uci';
'require ui';

var callServiceList = rpc.declare({
	object: 'service',
	method: 'list',
	params: [ 'name' ],
	expect: { '': {} }
});

var callLocalStatus = rpc.declare({
	object: 'luci.51ddns',
	method: 'status',
	expect: { '': {} }
});

function serviceState(data) {
	var instances = data && data['51ddns-agent'] && data['51ddns-agent'].instances;
	var names = instances ? Object.keys(instances) : [];
	var running = names.some(function(name) { return instances[name] && instances[name].running; });
	return {
		running: running,
		label: running ? '已启动' : '未运行',
		className: running ? 'alert-message success' : 'alert-message warning'
	};
}

function formatDate(value) {
	var date = new Date(value || '');
	if (isNaN(date.getTime()))
		return '同步中';
	return date.toLocaleString('zh-CN', {
		year: 'numeric', month: '2-digit', day: '2-digit',
		hour: '2-digit', minute: '2-digit', hour12: false
	});
}

function remainingState(value) {
	var expires = new Date(value || '');
	if (isNaN(expires.getTime()))
		return { label: '同步中', className: 'alert-message notice' };
	var milliseconds = expires.getTime() - Date.now();
	if (milliseconds <= 0)
		return { label: '已到期', className: 'alert-message error' };
	var hours = Math.ceil(milliseconds / 3600000);
	var label = hours > 48 ? ('约 ' + Math.ceil(hours / 24) + ' 天') : (hours + ' 小时');
	return {
		label: label,
		className: milliseconds <= 3 * 86400000 ? 'alert-message warning' : 'alert-message success'
	};
}

return view.extend({
	load: function() {
		return Promise.all([
			uci.load('51ddns'),
			L.resolveDefault(callServiceList('51ddns-agent'), {}),
			L.resolveDefault(callLocalStatus(), {})
		]);
	},

	render: function(data) {
		var state = serviceState(data[1]);
		var local = data[2] || {};
		var plan = local.plan || null;
		var remaining = remainingState(plan && plan.expires_at);
		var map = new form.Map('51ddns', '51DDNS 远程控制',
			'只需填写统一账户令牌即可自动创建设备并上线。设备名称、设备 ID 和连接参数均由平台自动处理。');
		var section = map.section(form.NamedSection, 'main', 'agent', '快速接入');
		section.anonymous = true;
		section.addremove = false;

		var status = section.option(form.DummyValue, '_status', '服务状态');
		status.rawhtml = true;
		status.cfgvalue = function() {
			return '<span class="' + state.className + '"><strong>' + state.label + '</strong></span>';
		};

		var version = section.option(form.DummyValue, '_version', '插件版本');
		version.cfgvalue = function() { return '0.1.4 / Agent 0.6.0'; };

		var planName = section.option(form.DummyValue, '_plan_name', '当前套餐');
		planName.cfgvalue = function() { return plan ? plan.product_name : '暂无有效套餐'; };

		var expiresAt = section.option(form.DummyValue, '_expires_at', '到期时间');
		expiresAt.cfgvalue = function() { return plan ? formatDate(plan.expires_at) : '未绑定'; };

		var remainingTime = section.option(form.DummyValue, '_remaining_time', '剩余时间');
		remainingTime.rawhtml = true;
		remainingTime.cfgvalue = function() {
			if (!plan)
				return '<span class="alert-message warning"><strong>请绑定或购买套餐</strong></span>';
			return '<span class="' + remaining.className + '"><strong>' + remaining.label + '</strong></span>';
		};

		var consoleLink = section.option(form.DummyValue, '_console', '控制台');
		consoleLink.rawhtml = true;
		consoleLink.cfgvalue = function() {
			return '<a class="btn cbi-button-action" href="https://console.51ddns.com/console#/workbench" target="_blank" rel="noopener">打开 51DDNS 控制台</a>';
		};

		var enabled = section.option(form.Flag, 'enabled', '启用');
		enabled.rmempty = false;
		enabled.default = enabled.disabled;

		var token = section.option(form.Value, 'account_token', '统一账户令牌');
		token.password = true;
		token.rmempty = false;
		token.placeholder = '51d_...';
		token.description = '在 51DDNS 用户后台顶部复制；同一账户的所有设备共用，不需要逐台生成。';

		return map.render();
	}
});
