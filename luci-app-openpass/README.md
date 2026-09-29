# luci-app-openpass

这是 OpenPass 的 LuCI 菜单插件，在 LuCI 的“服务 → OpenPass”中提供服务控制和页面入口：

- **关闭 / 开启 OpenPass**：完整停止或恢复 OpenPass 服务。关闭后不再运行管理后台和代理内核，停止代理和 DNS 接管，管理页面和设备自助页均无法访问；重启路由器后仍保持关闭。开启时恢复原有配置并启用开机启动。
- **打开管理页面**：打开 OpenPass 管理页面（`http://路由器地址:8787/`）。
- **打开设备自助页**：打开设备自助页面（`http://路由器地址:8787/choose`）。

页面每 5 秒刷新一次服务状态；操作期间禁止重复点击，状态读取失败时可以重试。OpenPass 停止后两个页面入口禁用，LuCI 页面本身仍可访问，以便重新开启服务。服务控制通过独立的 rpcd `openpass` 对象执行，不依赖 OpenPass 管理后台在线。

地址会根据当前访问 LuCI 的主机名自动生成，因此更换路由器 LAN 地址后不需要修改插件。页面入口会在新标签页打开；OpenPass 后端需要安装并监听 `8787` 端口。项目根目录的便携 tar.gz 会同时安装后端和这些 LuCI 文件，执行一次 `install.sh` 即可；SDK 生成的 ipk 包含 LuCI 页面及 rpcd 控制接口，不包含后端程序。

页面使用现代 LuCI 的 JavaScript view，菜单对应文件为 `/www/luci-static/resources/view/openpass/service.js`。安装时需要同时复制 `root/` 和 `htdocs/`，只安装菜单文件会导致 class file 404。支持 IPv4、IPv6 和主机名访问。

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

安装后刷新 LuCI，在 **服务 → OpenPass** 中即可查看状态、开启或关闭服务，以及打开两个页面。若开启后页面仍无法连接，请先确认 `/etc/init.d/openpass status` 正常且 `http://路由器地址:8787/` 可访问。
