# 51DDNS OpenWrt client privacy notice

Last updated: 2026-08-06

This notice describes the data processed by the open-source 51DDNS OpenWrt
client when it is connected to the commercial 51DDNS service operated by Shake
Cloud Inc. The control plane is vendor-hosted at `api.51ddns.com` and is not part
of this repository.

## Data processed

The client may send the following data to the 51DDNS control plane:

- the account token and the device credential used to authenticate requests;
- a random local installation identifier and the server-assigned device ID;
- the device platform, agent version, online state, and public IP address;
- the active plan and service capability status; and
- tunnel and route metadata required to generate and operate the requested FRP
  connections, plus limited diagnostics needed to troubleshoot failures.

The agent does not scan or upload arbitrary personal files from the router.
File-management, remote-desktop, SSH, and other remote-access connections are
created only when configured by the account user. The confidentiality of the
traffic carried through a tunnel depends on the selected application protocol;
users should prefer end-to-end encrypted protocols such as HTTPS and SSH.

## Remote configuration and trust

The agent periodically retrieves FRP configuration from `api.51ddns.com`. The
operator of that service can therefore select the relay endpoint and outbound
tunnel configuration used by an authenticated device. Users should install and
enable the client only if they trust the service operator. Stopping the
`51ddns-agent` service or uninstalling the package stops the managed tunnels.

## Local storage

The account token is stored in `/etc/config/51ddns` and copied to
`/etc/51ddns/device.token` with restricted permissions while the service runs.
The device ID and installation ID are also stored under `/etc/51ddns`. Both
locations are preserved across sysupgrade. Generated FRP configuration and
non-sensitive status data are stored under `/var/lib/51ddns`, which is temporary
on OpenWrt and is re-created after a reboot.

## Purpose, disclosure, and requests

The data is used to authenticate devices, provide remote-access features,
maintain service reliability, investigate failures, perform security auditing,
and prevent abuse. 51DDNS does not sell client telemetry as personal data. Data
may be processed by infrastructure providers needed to operate the service or
disclosed when required by law.

Users can disable or delete a device in the 51DDNS console. Account, access, and
data-deletion requests can be sent to `info@51ddns.com`.
