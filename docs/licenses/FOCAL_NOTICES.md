# Ubuntu 20.04 私有开发依赖记录

本记录对应 Z1 已验证的固定组合，不是正式分发包许可证批准或完整发行 SBOM。

- [机器可读清单](focal-dependencies.json)记录原生源码 URL/SHA256、仅用于构建的 APT 包版本/SHA256、Bottles/GNOME 签名提交、Soda 和已下载组件 URL/摘要。构建锁仍以 `scripts/focal-runtime.lock.json` 为准。
- [上游构建清单](focal/bottles-build-manifest.json)来自已安装的 Bottles 签名部署。Bottles 源提交为 `cf05edccf5b260767dd8b0084a57408635aca7b8`，元数据声明 GPL-3.0-only。
- `focal/` 保留已取得的 1533 份原文许可证/声明，共 10,408,485 字节，清单逐份记录摘要和来源。包含六个原生源码的 COPYING、GLib 的 LICENSES、Bottles/其 Python 依赖及 GNOME 平台随带声明。声明原文保留，不把整个运行时归为单一许可证。
- Flatpak/GLib/bubblewrap/xdg-dbus-proxy 的 COPYING 为 LGPL 2.1；OSTree COPYING 为 LGPL 2；Meson 为 Apache 2.0 且仅用于构建。具体文件的例外、组合和版权以原文及对应源码为准。

已下载组件包括 Soda 11.0-10、Mono 11.2.0、Gecko 2.47.4、DXVK、VKD3D、D7VK、NVAPI、LatencyFleX，以及 Bottles 应用预设下载的 Microsoft 字体。WineBridge 独立压缩包未下载，功能由 Bottles 应用部署提供；清单保留 `not_present`，不伪造摘要。

正式分发前仍须完成：Soda 对应构建源码和组合声明、Mono/Gecko 内嵌依赖的完整声明、Microsoft 字体 EULA 的随包方式选择、Electron/Chromium notices 和最终组装内容的 SBOM。字体不是因测试中已下载就可默认纳入分发包。项目自身许可仍由所有者决定。本轮没有生成安装器，也没有把这些未解决项写成已完成。

私有构建仍动态链接宿主 libc、libseccomp、libarchive、libcurl、libgpgme、libappstream 等库，并依赖内核用户命名空间、桌面显示/图形驱动和现有 `dbus-run-session`。源锁定、沙箱自检通过不等于在所有 Ubuntu 20.04 机器上独立运行。新固定前缀的实际 `ldd` 记录保存在验收目录 `fixed-runtime-library-boundary.json`。
