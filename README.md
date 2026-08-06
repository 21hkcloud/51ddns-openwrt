# 51DDNS for OpenWrt

51DDNS OpenWrt 客户端用于将路由器安全接入 51DDNS 自建控制平面，提供 HTTP/HTTPS 内网穿透、SSH 等远程应用、远程桌面、文件管理和动态 DNS 能力。

## 软件包

- `51ddns-agent`：由 OpenWrt 构建系统从本仓库 Go 源码编译的原生 `procd` 服务；使用官方软件源中的 `frpc` 建立隧道。
- `luci-app-51ddns`：位于“服务 → 51DDNS 远程控制”的 LuCI 管理页面。

用户只需在 LuCI 页面填写从 51DDNS 控制台复制的统一账户令牌。客户端会自动创建独立设备 ID，并将令牌保存到权限为 `0600` 的本地文件中，不会把令牌放入进程命令行。

## 从源码构建

将 `packages/51ddns-agent` 和 `luci-app-51ddns` 放入 OpenWrt SDK 或源码树的 `package/` 目录，然后按 OpenWrt 标准方式构建。Agent 使用 OpenWrt `golang-package.mk` 交叉编译，不下载 51DDNS 预编译可执行文件。

```sh
make package/51ddns-agent/compile V=s
make package/luci-app-51ddns/compile V=s
```

运行时的 FRP 客户端由 OpenWrt/ImmortalWrt 官方 `frpc` 软件包提供。

## 相关链接

- 官网：https://www.51ddns.com/
- 用户控制台：https://console.51ddns.com/
- 隐私说明：[PRIVACY.md](PRIVACY.md)
- 许可证：[Apache-2.0](LICENSE)

本仓库的 Apache-2.0 授权只适用于 51DDNS OpenWrt 客户端源代码与打包文件，不包含 51DDNS 控制平面、计费系统、管理后台或合作伙伴系统。
