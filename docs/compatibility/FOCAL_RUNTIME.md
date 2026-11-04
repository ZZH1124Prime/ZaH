# Ubuntu 20.04 / x86_64 固定依赖路线

2026-11-01。保留现有 Bottles 后端，**不要求用户更换系统**。私有 Flatpak、官方 Bottles 67.5 / GNOME 50 / Soda 11.0-10 已在 Z1 安装并运行两个自制无网络 Windows GUI：安装器、保存入口、并行、保存、精确单停和全停通过。详细证据及恢复/清理边界见 [实环境结果](../REAL_ENV_RESULTS.md)。这不代表任意 Windows 应用或安装版已完全兼容。

## 已落地的组合

| 组件 | 固定版本 | 在 Z1 的结果 |
|---|---|---|
| GLib | 2.88.4 | 使用本机编译器和 Python 3.8 成功构建；仅供私有运行时使用 |
| OSTree | 2026.5 | 编译成功，保留 GPG 签名验证；未安装 OS 部署服务 |
| Flatpak | 1.18.4 | 编译成功，`--version` 可运行 |
| Bubblewrap | 0.12.0，上游固定提交 | 私有 helper 构建成功；只读文件系统、独立网络 namespace 中运行 `/usr/bin/true` 成功 |
| xdg-dbus-proxy | 0.1.9，上游固定提交 | 私有 helper 构建成功、版本探测成功 |
| Meson（仅构建） | 1.6.1 | 使用系统 Python 3.8；没有安装新 Python |

使用 `system_helper=disabled` 构建官方支持的用户级安装方式，不提供 root 系统级安装助手。`seccomp` 和 `sandboxed_triggers` 保持启用，未使用关闭沙箱或绕过权限的方法。系统 bubblewrap、GLib、glibc、代理、Docker 和服务配置均未更改。

当前构建没有 Wayland security-context 协议支持，Z1 的 X11 路线是本轮目标；这不代表所有 Wayland 桌面已验证。宿主现有 libcurl/libarchive/libseccomp/portal/显卡用户空间仍参与运行，私有 GLib 并不等于整个操作系统静态封装。GNOME/Wine Flatpak runtime 能提供较新用户空间，但仍需要实际测试内核、图形、portal 与 32 位兼容。

选择已修复版本的依据：[Flatpak 1.18.4](https://github.com/flatpak/flatpak/releases/tag/1.18.4)、[Flatpak 安全公告](https://github.com/flatpak/flatpak/security/advisories)、[OSTree 2026.5](https://github.com/ostreedev/ostree/releases/tag/v2026.5)。已核对旧 Flatpak 1.16.6 缺少后续路径覆盖/删除及 sandbox 进程组相关修复、旧 OSTree 2020.8 缺少后续 checkout/whiteout 等修复。因此未把仍可下载的旧 PPA 包安装到宿主。[PPA 自身也声明旧 Ubuntu 包停止更新并有已知漏洞](https://github.com/flatpak/ppa-flatpak/blob/ppa/readme/README.md)。这不是“全部依赖没有任何漏洞”的保证。

## 构建与接入代码

- `scripts/focal-runtime.lock.json`：六项源码 URL、固定版本/提交、SHA-256，以及实际使用的 Focal 开发包固定版本和摘要。
- `scripts/focal-runtime.py build`：下载、验证、解包、私有编译/暂存安装。APT 只运行 `download`；不运行 `install/update`，不执行 `.deb` maintainer scripts，不注册任何服务或软件源。
- `verify`：目录所有权/写权限、完整文件摘要和链接边界检查。生成清单是本次构建的完整性记录，不是上游签名。
- `exec`：只为被启动的进程及其子进程追加私有 `bin` 搜索路径，不写 shell 配置，不全局设置 `LD_LIBRARY_PATH`，不启动或停止已有 manager。
- `scripts/test_focal_runtime.py`：归档穿越、链接、重复路径、运行时篡改、权限与环境保留回归。

开发依赖重建（目录必须尚不存在；这是编译依赖，不是制作 ZaH 安装包）：

```bash
python3 scripts/focal-runtime.py build --work-dir /绝对路径/新的私有构建目录
```

本次已下载缓存可通过 `--cache /tmp/zah-focal-flatpak-review-hi1sot3_` 复用，仍校验摘要。脚本要求本机已有编译工具及必要的基础开发库；缺少时停止并给出日志，不自动安装系统包。首次复现最后的权限检查发现宿主 umask=002 导致部分新建目录组可写，已修正构建 umask 和清单权限。修正后的完整脚本已在第三个全新目录从缓存源码走通构建、安装、完整性检查、版本检查、30 项上游测试与无网络沙箱自检，退出码 0。

早期验收产物：`/tmp/zah-focal-runtime-final-20261101/runtime`。以下命令保留用于复查历史构建；日常入口见下方“固定开发入口”：

```bash
python3 scripts/focal-runtime.py verify --prefix /tmp/zah-focal-runtime-final-20261101/runtime
python3 scripts/focal-runtime.py exec --prefix /tmp/zah-focal-runtime-final-20261101/runtime -- ./bin/zah doctor --json
```

这两个命令已经执行成功，doctor 找到该私有 Flatpak。`exec ... -- npm start` 只设置私有运行时搜索路径；本次 Windows 验收还使用独立 Flatpak 仓库、私有 session bus 和测试数据目录包装器，不能省略这些配置便声称默认用户界面可直接使用测试仓库。已有运行中的 manager 不会切换其 PATH；须由用户正常关闭后再启动，不能强杀。

产物包含绝对 RPATH/helper/service 路径，**不能直接复制到另一目录或当作已完成的随包依赖**。重建时指定最终位置；分发时另做可重定位处理、许可证和目标机器依赖检查。`/tmp` 可能在重启后清理，源码锁文件与构建脚本保留在仓库。

## 证据

原始构建：`/tmp/zah-focal-flatpak-review-hi1sot3_`。最终修正后完整复现：`/tmp/zah-focal-runtime-final-20261101`，顶层日志 `/tmp/zah-focal-runtime-final-20261101.log`。分别有 `glib-*.log`、`ostree-*.log`、`flatpak-*.log`。

- 原始构建及两次复现均完成 GLib/OSTree/Flatpak 编译和私有安装；最终脚本完整通过。
- Flatpak 上游 `test-context` 15、`test-ref-utils` 4、`test-exports` 11 项通过，共 30 项。不是整个上游集成测试套件。
- 新增构建器 13 项 fixture 测试通过；Go `internal/dependencies` 的普通测试、race、vet 通过。
- 无 loader 注入情况下 Flatpak/OSTree/helper 版本探测通过；Bubblewrap 无网络只读 `/usr/bin/true` 自检通过。
- 上述基础编译阶段未运行应用；后续经用户明确授权已运行 Bottles/Wine 和自制 GUI。没有运行未知 EXE，也没有向宿主桌面会话注册 D-Bus 激活文件。

来源验证程度：Flatpak 源码与官方 GitHub release asset digest 对照；GLib 对照 GNOME 官方 SHA256SUM；其他源码来自官方 HTTPS 发布/固定提交，并记录下载摘要，未额外验证发布者签名。PPA 候选曾单独验证签名索引及包摘要，**但未使用其旧 Flatpak/OSTree 作为运行组件**。构建用 Focal `.deb` 来自本机现有 APT 索引，仅解包头文件/构建工具；没有改软件源。

## 原安装计划与本轮执行范围

以下是基础编译完成时提出的计划，随后用户批准独立用户级安装及双 Bottle 测试。实际仓库为 `/home/zzh/Documents/Codex/2026-11-01/task/windows-validation-20261101/flatpak`，应用数据在相邻 `bottles-data`，使用私有 session bus。Bottles commit 为 `252c59e4451929e2e18e788f196a892c3d3c8c85231869c5fed0992f99fa016e`；GNOME 50 commit 为 `4337a389125f814785bd83f4ace01da5b6baa80fdcf5a9c81e60dcd03896c067`。Soda 归档 SHA256 为 `14633fabd180d308d2c1ebcb84a16f36d0484dded5809c64f8a7c93e654d7296`。官方仓库 GPG 校验保持开启；应用初始化自动安装的 Mono/Gecko/字体/图形组件另有逐项摘要及许可记录。

Flatpak 创建了原先不存在的 `~/.var/app/com.usebottles.bottles` 框架目录；最初一次 XDG 隔离配置未生效，写入了初始化缓存，但未在那里创建 Bottle。后续修正为在沙箱内部设置专用 XDG 目录。不能把“独立仓库”误写为“宿主 HOME 完全没有新增文件”。长期目录重建现已完成；正式发行安装配置仍未执行。

## 固定开发入口（后续收尾结果）

现在在 Z1 正常关闭旧 ZaH 后只需 `cd ~/zah && npm run dev`。入口自动选择已安装 Go 和私有配置，构建 Go/桌面/前端后打开窗口。已有构建使用 `npm start`；仅检查使用 `npm run check:runtime`。检查不打开窗口、不启动 manager、不下载。活动 manager 存在时拒绝覆盖构建，等待原窗口正常关闭后的空闲退出。

- 原生运行时：`~/.local/share/zah-focal-runtime-1.18.4/runtime`，在此绝对前缀重新编译，未移动 `/tmp` 二进制。完整构建、30 项上游测试和沙箱自检通过。
- 配置：`~/.local/share/zah-focal-desktop/profile.json`，0600；独立 Flatpak 仓库 `flatpak/`，依赖和后续 Bottle 数据 `data/bottles/`。从已授权缓存复制依赖，测试 Bottle、HOME、日志和恢复暂存均未复制。新 Bottle 根目录为空。
- `scripts/dev.py` 验证配置/固定 commit，向当前进程 PATH 添加私有适配器。`scripts/private_runtime.py` 只对 Bottles 调用使用 `dbus-run-session`，把私有 XDG 设置传入沙箱内部。Electron 保留原桌面 D-Bus、文件选择器和代理环境；未向桌面注册激活文件。
- 固定配置缺失则沿用系统依赖；明确选择的配置缺失、被移动、篡改或版本变化则失败，不悄悄换后端。可显式 `npm start -- --system-runtime` 选择系统依赖，或 `--runtime-profile /绝对路径/profile.json` 选择已准备配置。不要在已有应用环境之间随意切换后端仓库。
- 实测 `check:runtime`、沙箱内部私有 XDG 路径、Bottles CLI 空列表通过。首次发现缺少空 Bottle 根目录已修复并加入回归。CLI 会自行刷新上游目录；一次网络重置后仍返回 `{}`，不能声称完全离线。没有启动新的 GUI 或复跑双 Bottle 全流程；之前双 GUI 验收仍使用原独立测试配置。
- 每次调用结束临时会话退出，复查无本次私有 helper 残留。构建入口没有被本次测试实际执行，因此没有覆盖 `bin/zah`、`desktop/dist` 或 `frontend/dist`。

原生运行时和适配器包含绝对路径；源码或依赖目录搬迁后须重建/重新准备，不可直接作为发行包。启动时不执行更新；Bottles 自身仍可能请求组件目录/安装依赖，固定核心版本并不代表所有动态组件已永远锁定。[许可证与摘要记录](../licenses/FOCAL_NOTICES.md)列明已保存声明和正式分发尚欠内容。

下一阶段不需要先批准升级宿主 GLib、glibc 或安装旧 Flatpak PPA。当前已完成的构建中，APT 系统包 **新增 0、升级 0、删除 0**，系统服务启动/重启 **0**。

当时披露的用户级安装/会话影响如下（后续实际执行情况以上述记录为准）：

1. 将固定运行时在明确的长期私有目录重建；不移动现有临时产物，不覆盖任何系统二进制。
2. 通过该运行时建立独立测试 Flatpak 用户仓库，例如 `/home/zzh/.local/share/zah/flatpak-focal-validation`，仅向这个仓库加入官方 `https://dl.flathub.org/repo/flathub.flatpakrepo`。保持 GPG 校验。先读取实际 app/runtime commit、权限、下载/磁盘大小，再固定下载版本；当前尚未得到已部署 commit，不能编造具体 Bottles commit。
3. 仅安装 `com.usebottles.bottles/x86_64/stable` 及其列明的 runtime/extensions。当前上游构建声明 GNOME 50 / Wine base stable-25.08，这是构建清单信息，**不是已验收安装清单**。不运行全局 `flatpak update`、不自动升级其他用户应用。
4. 为独立测试会话提供私有 `flatpak-session-helper` / `flatpak-portal` 的 D-Bus 激活资料，优先独立测试 session bus；若必须注册到当前桌面用户会话，先列新增的精确文件及冲突检查，另行确认。现有系统 portal 不停止、不升级。
5. Bottle 默认应用目录可能写入 `~/.var/app/com.usebottles.bottles`；优先隔离测试用户数据，不能仅因 Flatpak 仓库隔离就声称所有应用数据隔离。正式执行前确认该路径无现有数据及实际隔离方案。随后固定一个官方可用 runner，用两个专属无真实数据的 Bottle 和自制无网络 PE 程序验收精准停止、安装入口和恢复；不借用现用 SakuraCat。

Bottles 官方包声明网络、X11/Wayland、音频及设备访问等权限；安装器原生 GUI、Wine 初始化和组件下载会在真实测试中发生，须明确接受此范围。不能保证任意 Windows 软件兼容，驱动/反作弊等仍不在承诺内。参考 [官方安装说明](https://docs.usebottles.com/getting-started/installation) 和 [Flathub 清单](https://github.com/flathub/com.usebottles.bottles/blob/master/com.usebottles.bottles.yml)。

回滚范围只包含本次新建、记录身份的运行时/仓库/私有会话及测试 Bottle；先停本次精确受管进程再删本次数据，不全局卸载、不 prune、不移除其他软件源或应用。保留下载及验收证据以便复现；最终清理状态见实环境结果。

## 安装版依赖入口（2026-11-04）

管理页“运行环境 → 依赖检查与安装”默认收起，继续使用原 Linux/Windows 选择、原生确认、后台任务和日志。Ubuntu 20.04 Windows 分支现在调用随包 `scripts/prepare-windows.py --install`：已有配置仅校验；没有配置时在当前用户目录构建固定私有 Flatpak，安装官方 Flathub Bottles/GNOME 及匹配显卡扩展，摘要校验后安装 Soda。GPG 校验保持开启，完成配置前要求 Bottles CLI 启动成功。

缺少宿主编译依赖时不会自动修改系统，而是打印准确的 apt-get 命令；下载/编译/CLI 失败会保留日志并返回失败。目录组写权限仍被拒绝，未放宽校验。首次创建 Bottle 时仍可能下载其他组件，因此此基础准备不等于全部 Windows 应用或离线环境已验证。

Linux 分支保留系统 APT 流程。Ubuntu 20.04 软件源可能无 Distrobox，界面失败区域提供官方固定版本源码安装命令；Docker 服务和当前用户权限仍由检查报告，不自动改用户组或宿主网络配置。
