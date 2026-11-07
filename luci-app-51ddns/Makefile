include $(TOPDIR)/rules.mk

LUCI_TITLE:=LuCI support for 51DDNS remote access
LUCI_URL:=https://github.com/21hkcloud/51ddns-openwrt
LUCI_DESCRIPTION:=Configure and monitor the 51DDNS remote access agent.
LUCI_DEPENDS:=+luci-base +curl +ca-bundle +flock
LUCI_EXTRA_DEPENDS:=51ddns-agent (>=0.6.7-r5)
LUCI_PKGARCH:=all

PKG_VERSION:=0.1.7
PKG_RELEASE:=23
PKG_PO_VERSION:=26.310.00009
PKG_LICENSE:=Apache-2.0
PKG_MAINTAINER:=Jinshuan Wang <21hkcloud@gmail.com>

# Keep the RPC helper executable when archives omit source mode bits.
define Build/Prepare/luci-app-51ddns
	find $(PKG_BUILD_DIR)/root $(PKG_BUILD_DIR)/htdocs -type d -exec chmod 0755 {} +
	find $(PKG_BUILD_DIR)/root $(PKG_BUILD_DIR)/htdocs -type f -exec chmod 0644 {} +
	$(INSTALL_BIN) ./root/usr/libexec/rpcd/luci.51ddns $(PKG_BUILD_DIR)/root/usr/libexec/rpcd/luci.51ddns
endef

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
