# OpenPass

OpenPass 是面向 x86 OpenWrt 的 sing-box 代理管理器，包含独立 Web 管理页面、设备自助页面和 LuCI 快捷入口。后端为 Go 静态程序，路由器不需要安装 Go、Node.js 或 Python。

## 一键安装或升级

在 **OpenWrt 路由器的 SSH 终端**以 root 执行：

```sh
wget -O /tmp/openpass-install.sh https://raw.githubusercontent.com/Missganggang/OpenPass/main/install.sh && sh /tmp/openpass-install.sh
```

若固件仅提供 curl：

```sh
curl -fL https://raw.githubusercontent.com/Missganggang/OpenPass/main/install.sh -o /tmp/openpass-install.sh && sh /tmp/openpass-install.sh
```

脚本从 [GitHub Releases](https://github.com/Missganggang/OpenPass/releases) 下载最新安装包，按 CPU 自动选择 amd64 或 386，并校验 SHA256。下载失败或校验不通过时不会继续安装。路由器需要能够访问 GitHub；也可使用下面的离线安装方式。

如果旧版启用 DNS 保护后出现“无法解析下载域名”，请先从电脑下载下方的离线包上传升级；安装过程不会为下载而放开明文 DNS。

支持使用 opkg 或 apk 的 OpenWrt x86_64 / 32 位 x86。脚本会通过系统软件源安装缺少的 sing-box、firewall4、nftables、TUN 模块、CA 证书和 LuCI；软件源和内核模块必须与固件匹配。要求 sing-box 1.12 或更新的 1.x 版本，目前实机验证为 OpenWrt 25.12 / sing-box 1.14。

部分定制固件的 firewall4 在缺少 `/etc/firewall.include` 时会误报重载失败。安装器仅在精确识别该问题并验证实际防火墙后，新建一个返回成功的空 hook，再次确认重载成功；已有文件不会被覆盖，也不修改固件的 fw4 程序。

安装器会关闭软件/硬件转发加速（flow offloading），避免加速流量绕过设备代理和断网保护，并开启 firewall4 的自动 include。首次原值保存在 `/etc/openpass/offloading-backup`，之后升级不会覆盖；这些设置不会自动恢复。关闭转发加速可能影响路由吞吐量。

安装后可访问：

- 管理页面：`http://路由器内网地址:8787/`
- 设备自助页：`http://路由器内网地址:8787/choose`
- LuCI：**服务 → OpenPass**，两个按钮分别打开以上页面。

设备自助页应由对应设备直接连接此路由器的局域网后访问；经过上级 NAT、反向代理或访客隔离时，路由器可能无法识别该设备的 MAC。访问地址例如 `http://10.0.0.1:8787/choose`。`openpass.lan` 需要额外配置本地 DNS，安装脚本不会自动建立此域名。

首次安装默认新设备**本地直连**，全局保护保持关闭；在管理页配置节点并启用后才开始接管流量。**升级保留原有节点、设备绑定、DNS 设置及全局开关**，不会覆盖 `/etc/openpass/state.json`。

**当前初版管理 API 尚无管理员鉴权，同一局域网内的设备可以管理配置。** 自助页按来源 IP 识别访问设备，但这不构成管理 API 的权限隔离。请仅在可信局域网使用，不要把管理端口映射到公网；正式访问控制将在后续版本提供。

固定安装某个版本：

```sh
OPENPASS_VERSION=v0.1.2 sh /tmp/openpass-install.sh
```

## 当前功能

- DoH 预设：阿里、腾讯、Cloudflare、Google；可按设备选择 DNS。
- 在线、离线、隐藏设备管理，展示内网 IP 和 MAC；在线状态按 DHCP 租约和实时邻居状态更新；支持节点绑定、解除绑定、直连和阻断。
- VLESS、VMess、Trojan、Shadowsocks、SOCKS5 节点导入，批量导入和订阅导入。
- 节点支持备注、URI 链接导出和 JSON 配置导出（导出文件包含节点凭据，请妥善保存）。
- Ping、TCPing、通过节点执行 URL 测试；国内/海外测试地址可修改。
- 全局代理、代理失败断网保护；新设备默认策略可切换为直连或阻断。
- 设备自助页展示访问设备的信息和绑定情况，允许设备选择可用节点；代理设备未单独指定 DNS 时默认使用 Cloudflare DoH，并通过绑定节点发送。

Ping/TCPing 只验证服务器的 ICMP/TCP 连通性，URL 测试才会验证节点协议和代理访问。部分服务器禁用 ICMP，Ping 超时并不代表节点不可用。

当前设备策略按 IPv4 地址执行，建议为绑定代理的设备配置 DHCP 静态租约，避免地址变化后策略失配。启用保护时会阻断 LAN 设备的 IPv6 出口，以防绕过当前 IPv4 策略。

## 离线安装与迁移

从 [Releases](https://github.com/Missganggang/OpenPass/releases/latest) 下载：

| 路由器架构 | 安装包 |
| --- | --- |
| x86_64 | `openpass-linux-amd64.tar.gz` |
| i386 / i686（32 位 x86） | `openpass-linux-386.tar.gz` |

同时下载 `SHA256SUMS`。以下以上传 x86_64 包为例，在电脑执行：

```sh
scp openpass-linux-amd64.tar.gz SHA256SUMS root@10.0.0.1:/tmp/
ssh root@10.0.0.1
```

然后在路由器执行（仅校验当前架构这一行）：

```sh
cd /tmp
awk '$2 == "openpass-linux-amd64.tar.gz"' SHA256SUMS | sha256sum -c - &&
mkdir -p /tmp/openpass-release &&
tar -xzf openpass-linux-amd64.tar.gz -C /tmp/openpass-release &&
sh /tmp/openpass-release/openpass/install.sh
```

校验显示 `OK` 后才会继续解压安装。离线安装仍需已有依赖；缺少依赖时脚本会尝试访问系统软件源。

备份旧路由器配置：

```sh
sh /usr/share/openpass/backup.sh /tmp/openpass-backup.tar.gz
```

把备份复制到新路由器，先安装 OpenPass，再恢复：

```sh
sh /usr/share/openpass/restore.sh /tmp/openpass-backup.tar.gz
```

备份包含节点凭据，请自行妥善保存；安装包和 Git 仓库不包含设备配置或节点凭据。

## 开发与发布

```sh
go test ./...
go vet ./...
go run ./cmd/openpass -listen 127.0.0.1:8787 -state ./state.json -web ./web -config ./sing-box.json
```

在 Linux / WSL 或具备 POSIX shell 的开发环境构建两个架构的完整便携包：

```sh
make packages VERSION=0.1.2
# dist/openpass-linux-amd64.tar.gz
# dist/openpass-linux-386.tar.gz
# dist/SHA256SUMS
```

包内包含后端、Web 页面、LuCI 的 root 和 htdocs 文件、防火墙声明、服务脚本及备份恢复工具。后端以 `CGO_ENABLED=0` 编译。使用架构标记阻止安装错误的包，并通过原子替换二进制支持正在运行的服务升级。

`/usr/share/nftables.d/ruleset-post/92-openpass-policy.nft` 在 firewall4 的同一事务中先载入持久的保护策略，再载入运行时策略，避免防火墙重载清除设备规则。首次安装或重启时允许相应文件尚未存在；请保持 firewall4 的 `auto_includes` 开启。

推送 `v*` 标签后，GitHub Actions 自动运行检查、构建上述三个文件并发布 GitHub Release。一键脚本始终使用相同资产名下载最新正式版本。源码推送和 PR 也运行 CI；编译产物位于 `dist/`，不提交到 Git。

若单独在 OpenWrt SDK 构建 LuCI 软件包，可把 `luci-app-openpass/` 复制到 SDK 的 `package/` 并执行 `make package/luci-app-openpass/compile V=s`。该包只提供两个入口，完整安装请使用本项目发布包。
