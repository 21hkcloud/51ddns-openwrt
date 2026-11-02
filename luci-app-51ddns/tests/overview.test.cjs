const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const source = fs.readFileSync(path.join(__dirname, '..', 'htdocs', 'luci-static', 'resources', 'view', '51ddns', 'overview.js'), 'utf8');

function renderOptions(local, running = true, config = {}) {
	const options = [];
	const removed = [];
 const polls = []; options.polls = polls; options.rpc = {};
 const removedPolls = []; options.removedPolls = removedPolls;
	options.removed = removed;
	class Map {
		constructor(_name, _title, description) { options.mapDescription = description; }
		section() {
			return {
				option(_kind, id) {
					const option = { id, disabled: '0', depends: () => {}, formvalue: () => config[id], validate: () => true };
					options.push(option);
					return option;
				},
			};
		}
		render() { return options; }
	}
	const form = { Map, NamedSection: class {}, DummyValue: class {}, Button: class {}, Flag: class {}, Value: class {} };
	const page = new Function('view', 'form', 'rpc', 'uci', 'L', '_', 'N_', 'window', 'E', 'poll', 'dom', source)(
		{ extend: value => value }, form, { declare: ({ method }) => () => options.rpc[method] instanceof Error ? Promise.reject(options.rpc[method]) : Promise.resolve(options.rpc[method] || {}) },
		{ get: (_package, _section, name) => config[name], unset: (...args) => removed.push(args) }, {}, value => value, () => '', {}, (tag, attributes, children) => ({tag, attributes, children, isConnected: true}),
 { add: (fn, interval) => polls.push({fn,interval}), remove: fn => removedPolls.push(fn) },
 { content: (node, children) => { node.children = children; } },
	);
	return page.render([{}, { '51ddns-agent': { instances: { main: { running } } } }, { identity_state: 'empty', ...local }, {}]);
}

test('router entry is actionable only after fresh public readiness and a strict host', () => {
	const host='router-12345678901234567890123456789012.cdn.cloud.toddns.com';
	for (const [state, hostname, running, fresh, shown] of [
		['ready',host,true,true,true],['assigned',host,true,true,false],['disabled',host,true,true,false],
		['ready',host,false,true,false],['ready',host,true,false,false],['ready','evil.example',true,true,false],
	]) {
		const local={router_web:{state,hostname},updated_at:new Date(Date.now()-(fresh?0:121000)).toISOString()};
		const widget=renderOptions(local,running).find(o=>o.id==='_router_web').renderWidget();
		const links=widget.children.filter(c=>c.tag==='a');assert.equal(links.length,shown?1:0);
		if(shown)assert.equal(links[0].attributes.href,'https://'+host);
	}
});

test('live readiness appears without reloading or changing unsaved form inputs', async () => {
 const host = 'router-12345678901234567890123456789012.cdn.cloud.toddns.com';
 const options = renderOptions({ router_web: { state: 'assigned', hostname: host }, updated_at: new Date().toISOString() }, true, { router_web_target: 'http://127.0.0.1:8088' });
 const widget = options.find(o => o.id === '_router_web').renderWidget();
 assert.equal(widget.children.some(n => n.tag === 'a'), false);
 assert.equal(options.polls.length, 1); assert.equal(options.polls[0].interval, 5);
 options.rpc.list = { '51ddns-agent': { instances: { main: { running: true } } } };
 options.rpc.status = { router_web: { state: 'ready', hostname: host }, updated_at: new Date().toISOString() };
 await options.polls[0].fn();
 assert.equal(widget.children.find(n => n.tag === 'a').attributes.href, 'https://' + host);
 assert.equal(options.find(o => o.id === 'router_web_target').formvalue(), 'http://127.0.0.1:8088');
 assert.deepEqual(options.removed, []);
});

test('live entry fails closed on stale state, stopped service, invalid host and RPC error', async () => {
 const host = 'router-12345678901234567890123456789012.cdn.cloud.toddns.com';
 const fresh = { router_web: { state: 'ready', hostname: host }, updated_at: new Date().toISOString() };
 for (const [status, running] of [[{ ...fresh, updated_at: '2000-01-01T00:00:00Z' }, true], [fresh, false], [{ ...fresh, router_web: { state: 'ready', hostname: 'evil.example' } }, true], [new Error('offline'), true], [{ ...fresh, router_web: { state: 'disabled', hostname: host } }, true]]) {
  const options = renderOptions(fresh);
  const widget = options.find(o => o.id === '_router_web').renderWidget();
  assert.ok(widget.children.some(n => n.tag === 'a'));
  options.rpc.list = { '51ddns-agent': { instances: { main: { running } } } };
  options.rpc.status = status;
  await options.polls[0].fn();
  assert.equal(widget.children.some(n => n.tag === 'a'), false);
 }
});

test('polling stops after the entry is detached and discards an in-flight response', async () => {
 const options = renderOptions({});
 const widget = options.find(o => o.id === '_router_web').renderWidget();
 await options.polls[0].fn();
 let release;
 options.rpc.status = new Promise(resolve => { release = resolve; });
 const before = widget.children;
 const pending = options.polls[0].fn();
 widget.isConnected = false;
 release({ router_web: { state: 'ready', hostname: 'router-12345678901234567890123456789012.cdn.cloud.toddns.com' }, updated_at: new Date().toISOString() });
 await pending;
 assert.equal(widget.children, before);
 assert.equal(options.removedPolls[0], options.polls[0].fn);
});

test('an unmounted discarded render does not leave a permanent poll callback', async () => {
 const options = renderOptions({});
 const widget = options.find(o => o.id === '_router_web').renderWidget();
 widget.isConnected = false;
 for (let i = 0; i < 3; i++) await options.polls[0].fn();
 assert.equal(options.removedPolls.length, 1);
});

test('recent device quota renders red guidance and the real plans link', () => {
	const options = renderOptions({ error_code: 'device_quota_exceeded', updated_at: new Date().toISOString() });
	const quota = options.find(option => option.id === '_quota');
	assert.ok(quota);
	assert.equal(quota.rawhtml, true);
	assert.match(quota.cfgvalue(), /color:#b91c1c/);
	assert.match(quota.cfgvalue(), /console\.51ddns\.com\/console#\/plans/);
	assert.match(quota.cfgvalue(), /already has a device record.*exact device ID.*Upgrade or purchase a plan only when adding another device/);
	assert.doesNotMatch(quota.cfgvalue(), /delete/i);
});

test('quota recency uses router time when the browser clock differs', () => {
	const updated_at = '2026-01-01T00:00:00Z';
	assert.equal(renderOptions({
		error_code: 'device_quota_exceeded', updated_at,
		router_time: '2026-01-01T00:00:30Z',
	}).some(option => option.id === '_quota'), true);
	assert.equal(renderOptions({
		error_code: 'device_quota_exceeded', updated_at,
		router_time: '2026-01-01T00:02:01Z',
	}).some(option => option.id === '_quota'), false);
});

test('quota guidance is suppressed for stale, unrelated, or stopped status', () => {
	const recent = new Date().toISOString();
	for (const [local, running] of [
		[{ error_code: 'invalid_token', updated_at: recent }, true],
		[{ error_code: 'device_quota_exceeded', updated_at: new Date(Date.now() - 121000).toISOString() }, true],
		[{ error_code: 'device_quota_exceeded', updated_at: recent }, false],
		[{ error_code: 'device_quota_exceeded' }, true],
	]) {
		assert.equal(renderOptions(local, running).some(option => option.id === '_quota'), false);
	}
});

test('relay failures show fixed retry guidance without claiming tunnel registration', () => {
	for (const [code, message] of [
		['frpc_config_write_failed', /check storage and permissions/],
		['frpc_start_failed', /check the frpc installation/],
		['frpc_process_exited', /attempting device reporting/],
	]) {
		const options = renderOptions({ error_code: code, updated_at: new Date().toISOString(), error: '<script>private-token</script>' });
		const relay = options.find(option => option.id === '_relay');
		assert.ok(relay);
		assert.notEqual(relay.rawhtml, true);
		assert.match(relay.cfgvalue(), message);
		assert.match(relay.cfgvalue(), /Retrying automatically/);
		assert.doesNotMatch(relay.cfgvalue(), /private-token|script|registered|connected/i);
		assert.equal(options.some(option => option.id === '_quota'), false);
	}
});

test('relay warnings obey router time and suppress stale, unknown and stopped states', () => {
	const updated_at = '2026-01-01T00:00:00Z';
	const current = { error_code: 'frpc_start_failed', updated_at, router_time: '2026-01-01T00:00:30Z' };
	assert.equal(renderOptions(current).some(option => option.id === '_relay'), true);
	for (const [local, running] of [
		[{ ...current, router_time: '2026-01-01T00:02:01Z' }, true],
		[{ ...current, error_code: 'constructor' }, true],
		[{ ...current, error_code: 'device_quota_exceeded' }, true],
		[{ ...current, updated_at: '' }, true],
		[current, false],
	]) {
		assert.equal(renderOptions(local, running).some(option => option.id === '_relay'), false);
	}
});

test('new router can use a pre-created UUID or leave it blank for automatic registration', () => {
	const options = renderOptions({}, false);
	const deviceID = options.find(option => option.id === 'device_id');
	assert.ok(deviceID);
	assert.equal(deviceID.rmempty, true);
	assert.match(options.mapDescription, /exact device UUID/);
	assert.match(options.mapDescription, /free device slot/);
	assert.match(deviceID.description, /A saved router ID is never cleared here/);
	assert.equal(deviceID.validate('main', ''), true);
	assert.equal(deviceID.validate('main', '00000000-0000-4000-8000-000000000001'), true);
	assert.match(deviceID.validate('main', 'not-a-uuid'), /valid device UUID/);
	assert.match(deviceID.validate('main', '00000000-0000-0000-0000-000000000000'), /valid device UUID/);
});

test('token correction and rotation retain an existing explicit device ID', () => {
	const tokenValue = 'private-account-token';
	const existingID = '00000000-0000-4000-8000-000000000001';
	const options = renderOptions({ identity_state: 'saved', saved_device_id: existingID }, true, { account_token: tokenValue, device_id: existingID });
	const token = options.find(option => option.id === 'account_token');
	const deviceID = options.find(option => option.id === 'device_id');
	assert.equal(token.password, true);
	assert.equal(token.validate('main', 'rotated-same-account-token'), true);
	assert.match(token.description, /Updating it keeps the saved device ID/);
	assert.match(token.description, /another account will be rejected/);
	assert.equal(deviceID.validate('main', existingID), true);
	assert.match(deviceID.validate('main', ''), /separate migration/);
	assert.match(deviceID.validate('main', '00000000-0000-4000-8000-000000000002'), /separate migration/);
	assert.equal(deviceID.remove('main'), undefined);
	assert.deepEqual(options.removed, []);
	assert.doesNotMatch(token.description, /private-account-token/);
	assert.doesNotMatch(deviceID.validate('main', ''), /private-account-token/);
});

test('a mistyped first token can be corrected without an assigned device ID', () => {
	const options = renderOptions({}, true, { account_token: 'mistyped-token', device_id: '' });
	const token = options.find(option => option.id === 'account_token');
	const deviceID = options.find(option => option.id === 'device_id');
	assert.equal(token.validate('main', 'corrected-token'), true);
	assert.equal(deviceID.validate('main', ''), true);
});

test('an unverified first-bind ID can be corrected or cleared while stopped', () => {
	const wrongID = '00000000-0000-4000-8000-000000000001';
	const correctedID = '00000000-0000-4000-8000-000000000002';
	const options = renderOptions({ identity_state: 'empty' }, false, { device_id: wrongID });
	const deviceID = options.find(option => option.id === 'device_id');
	assert.equal(deviceID.validate('main', correctedID), true);
	assert.equal(deviceID.validate('main', ''), true);
	deviceID.remove('main');
	assert.deepEqual(options.removed, [['51ddns', 'main', 'device_id']]);
	const running = renderOptions({ identity_state: 'empty' }, true, { device_id: wrongID });
	running.find(option => option.id === 'device_id').remove('main');
	assert.deepEqual(running.removed, []);
});

test('an automatically assigned ID remains protected when UCI has no explicit ID', () => {
	const options = renderOptions({ identity_state: 'saved', saved_device_id: '00000000-0000-4000-8000-000000000001' }, true, { account_token: 'private-account-token', device_id: '' });
	const deviceID = options.find(option => option.id === 'device_id');
	assert.equal(deviceID.validate('main', ''), true);
	assert.match(deviceID.validate('main', '00000000-0000-4000-8000-000000000002'), /saved device ID/);
});

test('unavailable identity prevents explicitly assigning an ID', () => {
	const options = renderOptions({ identity_state: 'unavailable', plan: { product_name: 'test plan' } });
	const deviceID = options.find(option => option.id === 'device_id');
	assert.match(deviceID.validate('main', '00000000-0000-4000-8000-000000000002'), /could not be verified/);
	assert.equal(deviceID.validate('main', ''), true);
});

test('stop the quota retry loop before assigning a pre-created identity', () => {
	const now = new Date().toISOString();
	const options = renderOptions({ error_code: 'device_quota_exceeded', updated_at: now }, true, {
		account_token: 'private-account-token', device_id: '',
	});
	const deviceID = options.find(option => option.id === 'device_id');
	assert.match(deviceID.validate('main', '00000000-0000-4000-8000-000000000002'), /Disable the agent/);
	const stopped = renderOptions({}, false, { account_token: 'private-account-token', device_id: '' });
	assert.equal(stopped.find(option => option.id === 'device_id').validate('main', '00000000-0000-4000-8000-000000000002'), true);
});

test('a quota snapshot never permits overwriting a saved identity', () => {
	const savedID = '00000000-0000-4000-8000-000000000001';
	const options = renderOptions({
		identity_state: 'saved', saved_device_id: savedID,
		error_code: 'device_quota_exceeded', updated_at: new Date().toISOString(),
	});
	const deviceID = options.find(option => option.id === 'device_id');
	assert.equal(deviceID.validate('main', savedID), true);
	assert.equal(deviceID.validate('main', ''), true);
	assert.match(deviceID.validate('main', '00000000-0000-4000-8000-000000000002'), /saved device ID/);
});

test('older or malformed RPC responses fail closed for an explicit ID', () => {
	for (const local of [{ identity_state: undefined }, { identity_state: 'saved', saved_device_id: 'invalid' }]) {
		const options = renderOptions(local);
		const deviceID = options.find(option => option.id === 'device_id');
		assert.match(deviceID.validate('main', '00000000-0000-4000-8000-000000000002'), /could not be verified/);
	}
});

test('a stale explicit ID can converge to the verified saved identity without replacing it', () => {
	const savedID = '00000000-0000-4000-8000-000000000001';
	const configuredID = '00000000-0000-4000-8000-000000000002';
	const options = renderOptions({ identity_state: 'saved', saved_device_id: savedID }, true, {
		enabled: '1', device_id: configuredID,
	});
	const deviceID = options.find(option => option.id === 'device_id');
	assert.equal(deviceID.validate('main', savedID), true);
	assert.match(deviceID.validate('main', configuredID), /saved device ID/);
	assert.match(deviceID.validate('main', '00000000-0000-4000-8000-000000000003'), /separate migration/);
	assert.match(deviceID.validate('main', ''), /separate migration/);
});

test('disabling remains available with conflicting, invalid, or unverified identity', () => {
	const configuredID = '00000000-0000-4000-8000-000000000002';
	for (const local of [
		{ identity_state: 'saved', saved_device_id: '00000000-0000-4000-8000-000000000001' },
		{ identity_state: 'unavailable' },
	]) {
		const options = renderOptions(local, true, { enabled: '1', device_id: configuredID });
		const deviceID = options.find(option => option.id === 'device_id');
		const enabled = options.find(option => option.id === 'enabled');
		enabled.formvalue = () => '0';
		assert.equal(deviceID.validate('main', configuredID), true);
		assert.equal(deviceID.validate('main', 'invalid'), true);
		assert.equal(deviceID.validate('main', ''), true);
		enabled.formvalue = () => '1';
		assert.notEqual(deviceID.validate('main', configuredID), true);
		assert.match(deviceID.validate('main', 'invalid'), /valid device UUID/);
	}
});
