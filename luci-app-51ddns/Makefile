include $(TOPDIR)/rules.mk

LUCI_TITLE:=LuCI support for 51DDNS remote access
LUCI_DEPENDS:=+luci-base +51ddns-agent
LUCI_PKGARCH:=all
PKG_VERSION:=0.1.3
PKG_RELEASE:=1
PKG_LICENSE:=Apache-2.0
PKG_MAINTAINER:=51DDNS <support@51ddns.com>
LUCI_URL:=https://www.51ddns.com/
LUCI_MAINTAINER:=51DDNS <support@51ddns.com>

define Package/luci-app-51ddns/postinst
#!/bin/sh
chmod 0755 "$${IPKG_INSTROOT}/usr/libexec/rpcd/luci.51ddns" 2>/dev/null || true
[ -n "$${IPKG_INSTROOT}" ] || /etc/init.d/rpcd restart
exit 0
endef

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
