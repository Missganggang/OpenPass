# OpenPass v0.1.12

LuCI 的“服务 → OpenPass”新增版本显示、更新检查和卸载功能。管理后台关闭时这些功能仍可使用。

- 在“打开设备自助页”右侧增加“检查更新”和“卸载 OpenPass”。
- 显示当前安装版本，检查 GitHub 最新正式版本，有新版时可确认升级。
- 升级下载对应 x86 架构的安装包并校验 SHA256，保留配置和服务关闭状态，显示进度和失败原因。
- 卸载需要确认，默认保留配置；也可选择删除节点、订阅及设备配置。
- 卸载先停止服务并清理 OpenPass 网络规则，清理失败时保留程序供重试；不卸载共享 sing-box 或其他代理插件。

在路由器 SSH 终端升级：

```sh
wget -O /tmp/openpass-install.sh https://raw.githubusercontent.com/Missganggang/OpenPass/main/install.sh && OPENPASS_VERSION=v0.1.12 sh /tmp/openpass-install.sh
```
