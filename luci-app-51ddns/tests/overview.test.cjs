const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const source = fs.readFileSync(path.join(__dirname, '..', 'htdocs', 'luci-static', 'resources', 'view', '51ddns', 'overview.js'), 'utf8');

function renderOptions(local, running = true) {
	const options = [];
	class Map {
		section() {
			return {
				option(_kind, id) {
					const option = { id };
					options.push(option);
					return option;
				},
			};
		}
		render() { return options; }
	}
	const form = { Map, NamedSection: class {}, DummyValue: class {}, Button: class {}, Flag: class {}, Value: class {} };
	const page = new Function('view', 'form', 'rpc', 'uci', 'L', '_', 'N_', 'window', source)(
		{ extend: value => value }, form, { declare: () => () => ({}) }, {}, {}, value => value, () => '', {},
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
