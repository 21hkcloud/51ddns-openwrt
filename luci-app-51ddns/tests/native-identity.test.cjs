const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const root = path.resolve(__dirname, '../..');
const init = path.join(root, 'packages/51ddns-agent/files/51ddns-agent.init');
const rpc = fs.readFileSync(path.join(root, 'luci-app-51ddns/root/usr/libexec/rpcd/luci.51ddns'), 'utf8');
const shell = process.env.DDNS_TEST_SH || (process.platform === 'win32' ? 'C:/Program Files/Git/bin/bash.exe' : 'sh');
const firstID = '00000000-0000-4000-8000-000000000001';
const secondID = '00000000-0000-4000-8000-000000000002';

function fixture(t, content) {
	const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ddns-identity-'));
	t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
	if (content !== undefined) fs.writeFileSync(path.join(dir, 'device.id'), content);
	fs.writeFileSync(path.join(dir, 'device.token'), 'existing-fixture-token\n');
	return dir.replaceAll('\\', '/');
}

function start(dir, requested, running = false, enabled = true) {
	return spawnSync(shell, ['-c', `
. "$DDNS_INIT"
SECRET_DIR="$DDNS_DIR"
RUNTIME_DIR="$DDNS_DIR/runtime"
uci_validate_section() {
 enabled="$DDNS_ENABLED"; account_token='new-fixture-token'; device_id="$DDNS_ID"
 control_url='https://api.51ddns.com'; refresh_seconds=30
 start_delay_seconds=0; max_active_relays=0; oem_voucher_file=''
}
logger() { printf '%s\\n' "$*" >&2; }
chown() { :; }
procd_open_instance() { printf 'started\\n'; }
procd_running() { [ "$DDNS_RUNNING" = 'yes' ]; }
procd_set_param() { :; }
procd_add_jail() { :; }
procd_add_jail_mount() { :; }
procd_add_jail_mount_rw() { :; }
procd_close_instance() { :; }
start_service
`], { encoding: 'utf8', env: { ...process.env, DDNS_INIT: init.replaceAll('\\', '/'), DDNS_DIR: dir, DDNS_ID: requested, DDNS_RUNNING: running ? 'yes' : 'no', DDNS_ENABLED: enabled ? '1' : '0' } });
}

test('startup refuses an identity assigned after the page was loaded before writing token or ID', t => {
	const dir = fixture(t, firstID + '\n');
	const result = start(dir, secondID);
	assert.equal(result.status, 1, result.stderr);
	assert.match(result.stderr, /conflicts with saved identity/);
	assert.doesNotMatch(result.stderr, /fixture-token|00000000/);
	assert.equal(fs.readFileSync(path.join(dir, 'device.id'), 'utf8'), firstID + '\n');
	assert.equal(fs.readFileSync(path.join(dir, 'device.token'), 'utf8'), 'existing-fixture-token\n');
	assert.equal(fs.existsSync(path.join(dir, 'runtime')), false);
});

test('startup permits the same saved ID and preserves its bytes', t => {
	const dir = fixture(t, firstID);
	const result = start(dir, firstID);
	assert.equal(result.status, 0, result.stderr);
	assert.match(result.stdout, /started/);
	assert.equal(fs.readFileSync(path.join(dir, 'device.id'), 'utf8'), firstID);
});

test('disabled service preserves identity and token even with a conflicting configured ID', t => {
	const dir = fixture(t, firstID + '\n');
	const result = start(dir, secondID, true, false);
	assert.equal(result.status, 0, result.stderr);
	assert.equal(result.stdout, '');
	assert.equal(fs.readFileSync(path.join(dir, 'device.id'), 'utf8'), firstID + '\n');
	assert.equal(fs.readFileSync(path.join(dir, 'device.token'), 'utf8'), 'existing-fixture-token\n');
	assert.equal(fs.existsSync(path.join(dir, 'runtime')), false);
});

test('first setup binds an exact ID only when the saved identity is absent or empty', t => {
	for (const old of [undefined, '']) {
		const dir = fixture(t, old);
		const result = start(dir, firstID);
		assert.equal(result.status, 0, result.stderr);
		assert.equal(fs.readFileSync(path.join(dir, 'device.id'), 'utf8'), firstID + '\n');
	}
});

test('blank explicit ID keeps the automatic-registration and saved-identity paths', t => {
	for (const old of [undefined, '', firstID + '\n']) {
		const dir = fixture(t, old);
		const result = start(dir, '');
		assert.equal(result.status, 0, result.stderr);
		assert.equal(fs.readFileSync(path.join(dir, 'device.id'), 'utf8'), old || '');
	}
});

test('invalid explicit ID is rejected before identity and token are written', t => {
	for (const invalid of ['invalid', firstID + '\nunexpected-content']) {
		const dir = fixture(t, '');
		const result = start(dir, invalid);
		assert.equal(result.status, 1, result.stderr);
		assert.equal(fs.readFileSync(path.join(dir, 'device.id'), 'utf8'), '');
		assert.equal(fs.readFileSync(path.join(dir, 'device.token'), 'utf8'), 'existing-fixture-token\n');
	}
});

test('non-regular identity path is rejected without changing token', t => {
	const dir = fixture(t);
	fs.mkdirSync(path.join(dir, 'device.id'));
	const result = start(dir, firstID);
	assert.equal(result.status, 1, result.stderr);
	assert.match(result.stderr, /not a regular file/);
	assert.equal(fs.readFileSync(path.join(dir, 'device.token'), 'utf8'), 'existing-fixture-token\n');
});

test('a running automatic-registration process must be stopped before assigning an ID', t => {
	const dir = fixture(t, '');
	const result = start(dir, firstID, true);
	assert.equal(result.status, 1, result.stderr);
	assert.match(result.stderr, /stop the agent/);
	assert.equal(fs.readFileSync(path.join(dir, 'device.id'), 'utf8'), '');
	assert.equal(fs.readFileSync(path.join(dir, 'device.token'), 'utf8'), 'existing-fixture-token\n');
});

test('RPC exposes only a valid saved UUID and an explicit identity state', t => {
	const functionSource = rpc.slice(rpc.indexOf('read_device_identity()'), rpc.indexOf('\nprint_status()'));
	for (const [content, expected] of [
		[undefined, 'empty|'], ['', 'empty|'],
		[firstID + '\n', 'saved|' + firstID], ['not-an-identity', 'unavailable|'],
		[firstID + '\nunexpected-content', 'unavailable|'],
	]) {
		const dir = fixture(t, content);
		const script = functionSource.replaceAll('/etc/51ddns/device.id', '$DDNS_DIR/device.id') + '\nread_device_identity\nprintf "%s|%s" "$status_identity_state" "$status_device_id"';
		const result = spawnSync(shell, ['-c', script], { encoding: 'utf8', env: { ...process.env, DDNS_DIR: dir } });
		assert.equal(result.status, 0, result.stderr);
		assert.equal(result.stdout, expected);
	}
	assert.match(rpc, /json_add_string saved_device_id "\$status_device_id"/);
	assert.doesNotMatch(functionSource, /device\.token|account_token/);
});
