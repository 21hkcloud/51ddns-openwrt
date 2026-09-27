const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const source = fs.readFileSync(path.join(__dirname, '..', 'htdocs', 'luci-static', 'resources', 'view', '51ddns', 'overview.js'), 'utf8');

function renderOptions(local, running = true, config = {}) {
	const options = [];
	class Map {
		constructor(_name, _title, description) { options.mapDescription = description; }
		section() {
			return {
				option(_kind, id) {
					const option = { id, validate: () => true };
					options.push(option);
					return option;
				},
			};
		}
		render() { return options; }
	}
	const form = { Map, NamedSection: class {}, DummyValue: class {}, Button: class {}, Flag: class {}, Value: class {} };
	const page = new Function('view', 'form', 'rpc', 'uci', 'L', '_', 'N_', 'window', source)(
		{ extend: value => value }, form, { declare: () => () => ({}) },
		{ get: (_package, _section, name) => config[name] }, {}, value => value, () => '', {},
	);
	return page.render([{}, { '51ddns-agent': { instances: { main: { running } } } }, local, {}]);
}

test('recent device quota renders red guidance and the real plans link', () => {
	const options = renderOptions({ error_code: 'device_quota_exceeded', updated_at: new Date().toISOString() });
	const quota = options.find(option => option.id === '_quota');
	assert.ok(quota);
	assert.equal(quota.rawhtml, true);
	assert.match(quota.cfgvalue(), /color:#b91c1c/);
	assert.match(quota.cfgvalue(), /console\.51ddns\.com\/console#\/plans/);
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

test('new router can use a pre-created UUID or leave it blank for automatic registration', () => {
	const options = renderOptions({});
	const deviceID = options.find(option => option.id === 'device_id');
	assert.ok(deviceID);
	assert.equal(deviceID.rmempty, true);
	assert.match(options.mapDescription, /exact device UUID/);
	assert.match(options.mapDescription, /free device slot/);
	assert.match(deviceID.description, /Leaving this blank does not clear a saved router ID/);
	assert.equal(deviceID.validate('main', ''), true);
	assert.equal(deviceID.validate('main', '00000000-0000-4000-8000-000000000001'), true);
	assert.match(deviceID.validate('main', 'not-a-uuid'), /valid device UUID/);
	assert.match(deviceID.validate('main', '00000000-0000-0000-0000-000000000000'), /valid device UUID/);
});

test('token correction and rotation retain an existing explicit device ID', () => {
	const tokenValue = 'private-account-token';
	const existingID = '00000000-0000-4000-8000-000000000001';
	const options = renderOptions({}, true, { account_token: tokenValue, device_id: existingID });
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

test('an automatically assigned ID remains protected when UCI has no explicit ID', () => {
	const options = renderOptions({}, true, { account_token: 'private-account-token', device_id: '' });
	const deviceID = options.find(option => option.id === 'device_id');
	assert.equal(deviceID.validate('main', ''), true);
	assert.match(deviceID.validate('main', '00000000-0000-4000-8000-000000000002'), /saved device ID/);
});

test('an existing plan also protects the identity when the token field is empty', () => {
	const options = renderOptions({ plan: { product_name: 'test plan' } });
	const deviceID = options.find(option => option.id === 'device_id');
	assert.match(deviceID.validate('main', '00000000-0000-4000-8000-000000000002'), /saved device ID/);
});

test('fresh quota error allows entering a pre-created device ID', () => {
	const now = new Date().toISOString();
	const options = renderOptions({ error_code: 'device_quota_exceeded', updated_at: now }, true, {
		account_token: 'private-account-token', device_id: '',
	});
	const deviceID = options.find(option => option.id === 'device_id');
	assert.equal(deviceID.validate('main', '00000000-0000-4000-8000-000000000002'), true);
});
