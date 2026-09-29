include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-openpass
PKG_VERSION:=0.1.10
PKG_RELEASE:=1
PKG_LICENSE:=MIT
PKG_MAINTAINER:=OpenPass contributors

LUCI_TITLE:=LuCI support for OpenPass
LUCI_PKGARCH:=all
LUCI_DEPENDS:=+luci-base +rpcd +flock

include $(TOPDIR)/feeds/luci/luci.mk

# The package contains only LuCI glue. The OpenPass daemon and its web
# application are installed separately by scripts/install.sh (or by the
# corresponding OpenWrt daemon package).
