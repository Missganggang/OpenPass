# luci-app-openpass

这是 OpenPass 的 LuCI 菜单插件。插件只负责在 LuCI 的“服务 → OpenPass”中提供两个按钮：

- **打开管理页面**：打开 OpenPass 管理页面（`http://路由器地址:8787/`）。
- **打开设备自助页**：打开设备自助页面（`http://路由器地址:8787/choose`）。

地址会根据当前访问 LuCI 的主机名自动生成，因此更换路由器 LAN 地址后不需要修改插件。按钮会在新标签页打开；OpenPass 后端需要安装并监听 `8787` 端口。项目根目录的便携 tar.gz 会同时安装后端和这些 LuCI 文件，执行一次 `install.sh` 即可；SDK 生成的 ipk 只包含 LuCI 快捷页面。

页面使用现代 LuCI 的 JavaScript view，菜单对应文件为 `/www/luci-static/resources/view/openpass/links.js`。安装时需要同时复制 `root/` 和 `htdocs/`，只安装菜单文件会导致 class file 404。支持 IPv4、IPv6 和主机名访问。

## 加入 OpenWrt SDK 或源码树

将本目录复制到 OpenWrt 源码树的 `package/luci-app-openpass/`，然后执行：

```sh
make menuconfig
# LuCI → Applications → luci-app-openpass 选为 <*>
make package/luci-app-openpass/compile V=s
```

生成的 `luci-app-openpass_*.ipk` 位于 `bin/packages/*/luci/`（不同 OpenWrt 版本目录可能略有不同）。将 ipk 上传到路由器后安装：

```sh
opkg update
opkg install /tmp/luci-app-openpass_*.ipk
rm -f /tmp/luci-indexcache
/etc/init.d/rpcd restart
/etc/init.d/uhttpd restart
```

也可以在源码树中使用 `make package/luci-app-openpass/compile` 生成 ipk 后手动复制安装。插件依赖 `luci-base`，不包含 `openpassd` 二进制或 sing-box；请先按项目根目录的 OpenWrt 部署说明安装 OpenPass 服务。

安装后刷新 LuCI，在 **服务 → OpenPass** 中即可看到两个按钮。若点击后无法连接，请先确认 `/etc/init.d/openpass status` 正常且 `http://路由器地址:8787/` 可访问。
