# ZaH Ubuntu 20.04 amd64 安装版

当前包：`ZaH-0.1.0-preview.20261104.4-ubuntu20.04-amd64.run`。摘要见同名 `.sha256`。

以普通桌面用户运行（不要 sudo）：

```bash
sh ./ZaH-0.1.0-preview.20261104.4-ubuntu20.04-amd64.run
~/.local/bin/zah
```

安装到 `~/.local/opt/zah/` 并建立应用菜单入口。升级保留旧版本和用户数据；切换版本前正常关闭旧 ZaH，等待管理器退出。包内含 Electron、Go 后端、界面、中英文字体和运行时准备脚本，不含用户应用、账号、Bottle 数据或预装的数 GB Windows 运行时。

管理页 → 运行环境 → **依赖检查与安装**，默认收起，点击展开、再次点击收起。Linux/Windows 共用原确认和日志流程。

- Linux：原 APT 安装功能继续处理系统源中的缺失包。Ubuntu 20.04 源可能没有 Distrobox，失败处提供官方源码安装命令；Docker 服务/当前用户访问权限由运行环境检查单独报告，不自动改变服务或用户组。
- Windows：Ubuntu 20.04 使用 `scripts/prepare-windows.py --install`。已有私有配置仅校验；新配置联网下载并编译固定 Flatpak，再下载官方 Bottles/GNOME/Soda 及匹配 NVIDIA 扩展。下载保持 GPG/摘要验证。缺少编译依赖时停止并在日志给出 apt-get 命令；不自动加源、升级宿主或修改代理。
- 基础运行时检查不代表所有 Windows 程序兼容。Bottles 创建环境时仍可能联网下载图形组件、Mono、Gecko 等。PotPlayer 首次打开、万达云嵌入式 GraphicsCapture 的已知限制见应用验收文档。

不是离线全依赖安装包，也不能把 Z1 上的检查等同于另一台全新机器已通过。新机器需可访问官方下载源及具备编译依赖。具体实测结果见 `docs/windows-user-acceptance-20261104.md` 与任务记录。
