# 51DDNS client for OpenWrt

This repository contains the open-source OpenWrt client for the commercial,
vendor-hosted 51DDNS remote-access service. The client connects to
`https://api.51ddns.com`; the 51DDNS control plane is not included in this
repository and cannot currently be self-hosted.

## What the client does

After an administrator enters a 51DDNS account token, the agent:

1. creates or restores a stable local installation identifier;
2. registers the router with the 51DDNS control plane;
3. reports basic device and public-address status;
4. downloads the FRP relay configuration assigned to that device; and
5. starts the OpenWrt `frpc` package as an outbound tunnel client.

This trust model matters: the operator of `api.51ddns.com` supplies the FRP
configuration used by the agent and therefore controls which relay endpoints
the client connects to. Only install and enable this package if you trust the
51DDNS service operator. Disable the service or remove the package to stop all
51DDNS-managed tunnels.

The OpenWrt package always runs the agent and its `frpc` children as the
dedicated, unprivileged `ddns51` user with `no_new_privs`. It also enters a
procd/ujail sandbox when the target provides `/sbin/ujail`; small-flash targets
without ujail remain supported and run unjailed but still unprivileged.
The account token is configured in `/etc/config/51ddns` and copied at runtime
to `/etc/51ddns/device.token` with restricted permissions. Device identity is
also stored under `/etc/51ddns`. Both locations are preserved across
sysupgrade. Runtime-generated FRP configuration is stored in the temporary
`/var/lib/51ddns` directory and is re-created after reboot.

## Packages

- `packages/51ddns-agent`: Go agent built from this source by the OpenWrt build
  system. It depends on the feed-provided `frpc` package and does not download a
  precompiled 51DDNS executable.
- `luci-app-51ddns`: architecture-independent LuCI interface. Its
  `LUCI_PKGARCH:=all` declaration produces one package shared by all supported
  router architectures. The same reviewed source is submitted to the official
  [`openwrt/luci`](https://github.com/openwrt/luci) feed.

## Build from source

Add this repository as a local OpenWrt feed, or copy both package directories
into an OpenWrt SDK or source tree, then build them normally:

```sh
make package/51ddns-agent/compile V=s
make package/luci-app-51ddns/compile V=s
```

For package installation, configuration, verification, troubleshooting and
removal, see [the OpenWrt user guide](docs/openwrt.md).

## Data and privacy

The client sends the account/device credential, installation and device
identifiers, platform and agent version, public IP address, operational status,
and tunnel configuration metadata needed to provide the service. It does not
scan or upload arbitrary files from the router. Traffic content visible to a
relay depends on the protocol selected by the user; use end-to-end encrypted
protocols such as HTTPS and SSH whenever possible.

See [PRIVACY.md](PRIVACY.md) for the English client privacy notice.

## Links

- Service website: https://www.51ddns.com/
- User console: https://console.51ddns.com/
- Support and data requests: info@51ddns.com
- Client license: [Apache-2.0](LICENSE)

The Apache-2.0 license applies to the client source and packaging files in this
repository. It does not cover the 51DDNS control plane, billing system,
administration console, or partner portal.
