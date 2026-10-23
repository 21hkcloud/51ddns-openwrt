# 51DDNS on OpenWrt

This guide covers the open-source `51ddns-agent` package for the commercial,
vendor-hosted 51DDNS remote-access service. The agent connects outbound to
`https://api.51ddns.com` and starts the feed-provided `frpc` client with tunnel
configuration assigned by the 51DDNS control plane.

## Requirements

- an OpenWrt device with Internet access and a correct system clock;
- a 51DDNS account and account token from the 51DDNS user console;
- the `51ddns-agent`, `frpc` and `ca-bundle` packages; and
- optionally, `luci-app-51ddns` for browser-based configuration.

The LuCI package is maintained and reviewed separately in the official
`openwrt/luci` feed. It is not included in the agent source archive.

### OpenWrt 24.10 and iStore

Agent 0.6.6-r2 supports the official OpenWrt 24.10 `frpc` 0.51.3 package.
It detects the installed FRPC version and generates compatible INI for exactly
0.51.3; FRPC 0.52.0 or later uses TOML. Unsupported versions and configurations
that cannot be represented safely are rejected. Do not force dependencies.
Older Agent 0.6.4 packages do not have this compatibility path.

The validated combinations are OpenWrt 24.10.8 with FRPC 0.51.3 and OpenWrt
25.12.5 with FRPC 0.66.0, on x86_64 and aarch64. These are isolated native
installation, upgrade, rollback and synthetic connectivity tests, not a claim
that every customer firmware or router has been tested.

The self-hosted signed feed currently supplies Agent 0.6.6-r2 and LuCI
0.1.6-r1 for these four targets. The new iStore package update remains under
review. Until its public index actually lists the new version, an iStore
installation can still select the old Agent 0.6.4-r1. OpenWrt packages/LuCI
upstream acceptance and iStore publication are separate processes.

Once the iStore update is indexed, use iStore with the normal OpenWrt feeds
enabled. The Agent declares `frpc >=0.51.3`; the official feed supplies this
dependency, so adding a vendor FRPC feed is not a prerequisite for the new
version. Do not add shared FRPC to iStore's application-uninstall dependency
list. On a firmware that lacks a compatible official dependency, stop and
check its feeds instead of bypassing package validation.

For the already published self-hosted feed, administrators may explicitly
choose the installer below. It adds the signed 51DDNS feed and installs the
Agent and LuCI; it changes router configuration and should only be run by the
router administrator. It is not an iStore publication check.

中文说明：新版 Agent 0.6.6-r2 已适配官方 frpc 0.51.3（自动生成 INI），
0.52.0 及以上使用 TOML。商店新版仍待审核，旧版 0.6.4 不具备此适配。
商店更新后应保留正常官方软件源，由包管理器选择满足最低版本的 frpc，
无需为了新版本额外安装第三方 frpc；不要强制忽略依赖。
以下脚本是主动选择自建签名源的安装方式，仅由路由器管理员执行。

```sh
uclient-fetch -q -O /tmp/51ddns-repository.sh \
  https://www.51ddns.com/downloads/openwrt/install-repository.sh
sh /tmp/51ddns-repository.sh
```

Before entering an account token, check installed versions with the package
manager used by the firmware:

```sh
# OpenWrt 24.10 / opkg
opkg list-installed | grep -E '^(51ddns-agent|frpc|luci-app-51ddns) '
# OpenWrt 25.12 / apk
apk list --installed | grep -E '^(51ddns-agent|frpc|luci-app-51ddns)-'
```

Do not install an APK on an IPK-based system. MIPS and historical feed entries
were not part of this four-target update; do not infer support from another
architecture's result.

## Install

After the packages are available in the configured feed, install them with the
package manager used by the OpenWrt release:

```sh
# OpenWrt releases using apk
apk update
apk add 51ddns-agent luci-app-51ddns

# OpenWrt releases using opkg
opkg update
opkg install 51ddns-agent luci-app-51ddns
```

The LuCI package is optional. The agent can be configured entirely with UCI.

## Configure with LuCI

Open **Services -> 51DDNS Remote Access**, paste the account token, enable the
service, and select **Save & Apply**. The page shows the local agent process
state, agent version, current plan, expiry information and a link to the 51DDNS
console. A running process alone does not prove that the router has connected to
the control plane; confirm that the device appears in the console.

## Configure with UCI

Do not put the token in shell history on shared systems. The following example
uses a placeholder that must be replaced locally:

```sh
uci set 51ddns.main.enabled='1'
uci set 51ddns.main.account_token='REPLACE_WITH_ACCOUNT_TOKEN'
uci commit 51ddns
/etc/init.d/51ddns-agent enable
/etc/init.d/51ddns-agent restart
```

The default control-plane URL is `https://api.51ddns.com`. Advanced settings
are optional:

```sh
uci set 51ddns.main.refresh_seconds='30'
uci set 51ddns.main.start_delay_seconds='0'
uci set 51ddns.main.max_active_relays='0'
uci commit 51ddns
/etc/init.d/51ddns-agent restart
```

`max_active_relays=0` means no explicit limit except the agent's built-in
safety limit. On MIPS targets, the init script defaults to one active relay to
reduce memory pressure unless a different value is configured.

## Verify operation

```sh
/etc/init.d/51ddns-agent status
logread -e 51ddns-agent
51ddns-agent --version
```

A successful start reports the agent running and, after registration, the
device appears in the 51DDNS console. Tunnel login messages are emitted by the
feed-provided `frpc` process.

## Troubleshooting

If the service does not start:

1. verify that `enabled` is `1` and the account token is present;
2. confirm DNS, HTTPS connectivity and the router clock;
3. check `logread -e 51ddns-agent` for configuration or HTTP errors;
4. confirm that `/usr/bin/frpc` exists; and
5. restart the service after correcting the configuration.

Useful commands:

```sh
uci show 51ddns
date
nslookup api.51ddns.com
/etc/init.d/51ddns-agent restart
```

Before sharing logs, remove account tokens, device credentials, public
addresses and any customer-specific tunnel information.

## Security and trust boundary

The service operator controls the authenticated FRP relay configuration
returned to the agent. Install and enable the package only if you trust the
51DDNS service operator. Prefer end-to-end encrypted application protocols such
as HTTPS and SSH for traffic carried through a tunnel.

The package always runs under the unprivileged `ddns51` user with
`no_new_privs`. It uses a procd/ujail sandbox where `/sbin/ujail` is available.
On small-flash targets without ujail, it remains unprivileged but is not jailed.

The account token is configured in `/etc/config/51ddns` and copied at runtime
to `/etc/51ddns/device.token` with restricted permissions. Stable identifiers
are also stored under `/etc/51ddns`. Both locations are preserved across
sysupgrade. Generated FRP configuration is stored under `/var/lib/51ddns`,
which is temporary on OpenWrt. See [the privacy notice](../PRIVACY.md) for the
data processed by the client.

## Disable or remove

To stop all 51DDNS-managed tunnels without removing local configuration:

```sh
/etc/init.d/51ddns-agent disable
/etc/init.d/51ddns-agent stop
```

To uninstall the packages:

```sh
# apk-based releases
apk del luci-app-51ddns 51ddns-agent

# opkg-based releases
opkg remove luci-app-51ddns 51ddns-agent
```

Package removal does not automatically erase preserved credentials. After the
service is stopped and the package is removed, delete them only if the device
should no longer retain its 51DDNS identity:

```sh
rm -f /etc/config/51ddns
rm -rf /etc/51ddns
```

Also disable or delete the device in the 51DDNS console if it should no longer
be authorized.

## Support

- Service website: https://www.51ddns.com/en
- User console: https://console.51ddns.com/console/en
- Support and data requests: info@51ddns.com
