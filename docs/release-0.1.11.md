# OpenPass v0.1.11

本版本新增订阅管理。导入订阅地址时可以填写名称（例如 Hello）和备注，在节点列表的订阅区域查看和管理各个来源。

- 点击“查看节点”，只显示该订阅的节点；可清除筛选返回全部节点。
- 支持手动刷新订阅、修改名称/备注和删除订阅。
- 刷新保留连接信息未变节点的 ID 和本地备注；订阅中的重复节点保持独立 ID。
- 删除订阅或刷新时若会移除已绑定节点，提示先解绑设备；检查涵盖在线、离线和隐藏设备。
- 兼容旧节点状态文件，升级保留现有节点、设备绑定和 DNS 设置。

升级时在 OpenWrt 的 SSH 终端运行：

```sh
wget -O /tmp/openpass-install.sh https://raw.githubusercontent.com/Missganggang/OpenPass/main/install.sh && OPENPASS_VERSION=v0.1.11 sh /tmp/openpass-install.sh
```

也可以下载本发布的 amd64 或 386 离线安装包，按照 README 中的校验和安装步骤操作。订阅当前使用手动刷新。
