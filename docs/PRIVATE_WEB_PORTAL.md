# 私有 Windows 环境网页打开通道

2026-11-02，修订 `direct-http-v2`。仅开发态 `scripts/private_runtime.py` 适配器。

## 当前行为

按用户最新要求，应用发起普通 HTTP/HTTPS OpenURI 后**直接调用宿主默认浏览器，不弹额外确认窗口，不设域名白名单**。原始 URI 原样传给 GIO，没有 shell 拼接或执行。file、命令、自定义 scheme、控制字符、反斜杠、错误转义/主机/端口仍拒绝；标准 URI 最长 8192 ASCII 字符，国际化域名/路径分别使用 punycode/百分号编码。

每次启动的私有 D-Bus 仅提供 OpenURI v1/Request，不透传宿主总线，不增加沙箱文件系统权限。宿主进程保留原 HOME、XDG、桌面总线；私有 daemon 和 Flatpak 子进程先移除继承的 DBUS_*，Flatpak 只得到私有总线地址。Flatpak 命令中的 `--env=HOME=测试目录` 和沙箱内部 XDG_DATA_HOME 不改变宿主浏览器环境。保持既有私有 Flatpak 1.18.4 和固定提交核验。

允许最多 16 个并行待完成打开请求，每次 30 秒超时。Request.Close 仅允许原请求连接调用；取消不能撤回已交给浏览器的页面。**调用者正常退出不代表取消**：flatpak-xdg-utils 的 xdg-open 本来就会在方法返回后退出，已受理请求继续直到完成或超时。启动命令退出时等待已受理请求完成；正常退出/终止信号会清理本次 daemon 和请求。

## 本次静默失败的实证

已安装 GNOME 50 runtime 的 `bin/xdg-open` 只同步调用 OpenURI，得到方法返回即退出，不等待 Response。旧门户在 NameOwnerChanged 时取消请求，所以会在确认窗口显示前或刚显示后销毁请求，且旧代码没有阶段日志。

使用**本机实际部署的 xdg-open ELF + 同部署的动态加载器/库**，在临时总线执行前后对比；浏览器端替换为假接口，不访问任何网站：

- 旧版：xdg-open 退出码 0；确认窗口创建后立即销毁；浏览器派发 0 次。
- 修正版：xdg-open 退出码 0；调用者退出后仍继续；假浏览器派发 1 次并完成。

另用真实短命 gdbus 客户端复现了旧版“完全没有确认窗口、没有派发，但返回码 0”。该确定缺陷与用户现象一致。检查时没有存活的 QQ/Wine/私有门户进程，也没有本次点击的门户日志，因此不能证明它是用户那次失败的唯一因素；不把 OLE/lsteamclient 通用日志当作根因。

只读核查：当前宿主 HOME=/home/zzh，XDG_DATA_HOME/XDG_CONFIG_HOME 未被测试目录覆盖，桌面总线存在；HTTP 和 HTTPS 的默认处理程序均为 google-chrome.desktop。未实际启动 Chrome。原 GTK 确认逻辑已删除，不再依赖确认窗口可见性。

## 下一次点击的可观察结果

日志输出到启动终端 stderr，不写 URL、路径、query、fragment 或请求 token：

- `ready` 且 `implementation=direct-http-v2`：新版门户已注册。
- `host-context`：显示宿主 HOME/XDG 路径和桌面总线/显示是否存在，不输出总线地址。
- `request-received`：收到应用 OpenURI；只记录内部序号和 scheme。
- `launch-started` / `host-dispatch`：开始交给宿主，记录默认处理程序 ID。
- `caller-exited` + `continue-launch`：短命客户端退出，继续受理。
- `request-finished`，response=0 表示 GIO 接受派发；不保证远端网页加载成功。
- `request-rejected`、`host-launch-failed`、`launch-exception`、`request-timeout` 或 response=2：失败阶段；GIO 错误记录 domain/code/type，避免错误原文泄露登录 URL。

若有 ready 但点击后没有 request-received，应继续查 Wine→xdg-open→私有总线链路；不能仅因没有 portal missing 就宣称成功。

## 验证与用户操作

`/usr/bin/python3 -B -m unittest discover -s scripts -p 'test_private*.py'`：28 项通过，包括真实短命 D-Bus 客户端、断连后继续打开、延迟回调、超时/Close、并发、日志敏感信息不泄露及退出清理。另完成上述真实部署 xdg-open 前后对比。未运行 QQ、创建 Bottle、打开网页或更改系统配置；未覆盖 bin/dist 或执行打包。

用户正常退出旧安装器后，用**原命令、同一个 profile/Bottle**重开一次，保留已有字体和 288 DPI。无需重建 Electron/Go。看到 direct-http-v2 的 ready 后，点击安装器真实协议链接，应直接打开浏览器；若失败，提供这一点击附近的 ZaH web portal 日志。不要用腾讯通用网页代替实际链接，不自动接受协议。

尚未验收：本次修改后的 QQ/Wine 完整点击链路、真实浏览器页面加载、登录回调。自定义协议回调、内嵌网页引擎、应用自身联网和音频仍是独立兼容项，不通过全开宿主总线或任意 scheme shell 执行来代替实现。

上游行为依据：[flatpak-xdg-utils 的 xdg-open.c](https://github.com/flatpak/flatpak-xdg-utils/blob/main/src/xdg-open.c)。协议依据：[OpenURI](https://flatpak.github.io/xdg-desktop-portal/docs/doc-org.freedesktop.portal.OpenURI.html)、[Request](https://flatpak.github.io/xdg-desktop-portal/docs/doc-org.freedesktop.portal.Request.html)。
