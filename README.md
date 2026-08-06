# 51DDNS for OpenWrt

51DDNS OpenWrt 客户端用于把路由器安全接入 51DDNS 自建控制平面，提供 HTTP/HTTPS 内网穿透、SSH 等远程应用、远程桌面、文件管理和动态 DNS 能力。

## 软件包

- `51ddns-agent`：原生 `procd` 服务、UCI 配置、51DDNS Agent 与 FRP 客户端。
- `luci-app-51ddns`：位于“服务 → 51DDNS 远程控制”的 LuCI 管理页面。

用户只需在 LuCI 页面填写从 51DDNS 控制台复制的统一账户令牌。客户端会自动创建独立设备 ID，并把令牌保存到权限为 `0600` 的本地文件中，不会把令牌放入进程命令行。

## 支持范围

| OpenWrt | 包管理器 | 架构 | 状态 |
| --- | --- | --- | --- |
| 25.12.5 | APK | `aarch64_generic` | 已完成官方镜像 QEMU 全流程验证 |
| 25.12.5 | APK | `x86_64` | 已构建，待以当前版本重新发布 |
| 24.10.x | opkg/IPK | `mipsel_24kc` | 已完成 MT7621 实机安装验证；不属于 iStore 当前收录架构 |

## 构建

将 `packages/51ddns-agent` 和 `luci-app-51ddns` 放入 OpenWrt SDK 或源码树的 `package/` 目录，然后按 OpenWrt 标准方式构建。Agent 的可复现发布归档由 51DDNS 官网提供，并在 Makefile 中固定 SHA-256。

## 相关链接

- 官网：https://www.51ddns.com/
- 用户控制台：https://console.51ddns.com/
- 隐私说明：[PRIVACY.md](PRIVACY.md)
- 许可证：OpenWrt 客户端代码采用 Apache-2.0，见 [LICENSE](LICENSE)

51DDNS 服务端、管理后台、计费及渠道结算系统不包含在本仓库的 Apache-2.0 授权范围内。
