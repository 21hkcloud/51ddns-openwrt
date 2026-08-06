include $(TOPDIR)/rules.mk

PKG_NAME:=51ddns-agent
PKG_VERSION:=0.5.3
PKG_RELEASE:=2

ifeq ($(ARCH),x86_64)
  AGENT_ARCH:=amd64
  PKG_HASH:=53f584ac01a140c61ab60aa9e524d0efdeabf9430f293201c5cfb3a24633eb84
else ifeq ($(ARCH),aarch64)
  AGENT_ARCH:=arm64
  PKG_HASH:=298760e3aa35ff2e3d16ee809ecf823a70d0b6811ab8c735260d654255cf6c74
else ifeq ($(ARCH),mipsel)
  AGENT_ARCH:=mipsle
  PKG_HASH:=61e8d6ea11c65ab29c0ec4b9649aeb4ad8b0c1660649e76d96494d4c934a71d2
else
  AGENT_ARCH:=unsupported
  PKG_HASH:=skip
endif

PKG_SOURCE:=51ddns-agent_$(PKG_VERSION)_linux_$(AGENT_ARCH).tar.gz
PKG_SOURCE_URL:=https://www.51ddns.com/downloads/
PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)
PKG_LICENSE:=Apache-2.0
PKG_MAINTAINER:=51DDNS <support@51ddns.com>

include $(INCLUDE_DIR)/package.mk

# The published Agent archive intentionally has the executables at its root
# (./51ddns-agent and ./frpc). OpenWrt's default prepare rule expects source
# archives to contain a top-level directory and therefore extracts one level
# above PKG_BUILD_DIR. Keep the release archive layout stable and unpack it
# explicitly into the package build directory.
define Build/Prepare
	rm -rf $(PKG_BUILD_DIR)
	$(INSTALL_DIR) $(PKG_BUILD_DIR)
	$(TAR) -C $(PKG_BUILD_DIR) -xzf $(DL_DIR)/$(PKG_SOURCE)
endef

define Package/51ddns-agent
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=VPN
  TITLE:=51DDNS remote access agent
  URL:=https://www.51ddns.com/
  DEPENDS:=@(aarch64||x86_64||mipsel) +ca-bundle
endef

define Package/51ddns-agent/description
 Securely connects an OpenWrt device to the self-hosted 51DDNS control plane.
endef

define Package/51ddns-agent/conffiles
/etc/config/51ddns
endef

define Package/51ddns-agent/postinst
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] && exit 0

# Devices installed by the original one-click script already own these files.
# apk protects existing files below /etc by writing the packaged copy as
# .apk-new. Promote the new service definition while keeping the user's UCI
# configuration, account token and device identity untouched.
if [ -f /etc/init.d/51ddns-agent.apk-new ]; then
	mv -f /etc/init.d/51ddns-agent.apk-new /etc/init.d/51ddns-agent
	chmod 0755 /etc/init.d/51ddns-agent
fi
# opkg and apk both preserve the live UCI file and place the package default
# beside it when the user has changed the token or other settings. Runtime
# defaults are supplied by the init script, so these generated copies are not
# needed and would otherwise accumulate after a reinstall or upgrade.
rm -f /etc/config/51ddns.apk-new /etc/config/51ddns-opkg \
	/etc/51ddns/startup.pending

/etc/init.d/51ddns-agent enable >/dev/null 2>&1 || true
/etc/init.d/51ddns-agent restart >/dev/null 2>&1 || true
exit 0
endef

define Build/Compile
endef

define Package/51ddns-agent/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/51ddns-agent $(1)/usr/bin/51ddns-agent
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/frpc $(1)/usr/bin/51ddns-frpc
	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/51ddns.config $(1)/etc/config/51ddns
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/51ddns-agent.init $(1)/etc/init.d/51ddns-agent
	$(INSTALL_DIR) $(1)/usr/libexec
	$(INSTALL_BIN) ./files/51ddns-agent-start $(1)/usr/libexec/51ddns-agent-start
	$(INSTALL_DIR) $(1)/etc/51ddns $(1)/var/lib/51ddns
endef

$(eval $(call BuildPackage,51ddns-agent))
