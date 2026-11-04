# 安装包制作前的就绪清单

2026-11-02 图形安装改造：新版源码支持 x86/AMD64 EXE，保留原文件名，ZIP保留完整目录；所有新版本安装后必须图形选择并确认入口。Windows保存相对 EXE/CWD/字面参数，并保留到迁移 profile；Linux保存所选 desktop/AppImage 的类型与摘要，desktop通过原生GIO启动。开源 Noto Sans CJK 字体随Go程序携带，许可证及来源位于 `internal/backend/fonts/`；仅加入实测有效的 Microsoft YaHei、微软雅黑、MS Shell Dlg 三个别名。新建环境可选DPI，不改宿主显示，不固定全局288。

真实证据边界：Soda 11.0-10 已运行官方32位QQ音乐，用户确认英文 Microsoft YaHei 别名补齐后当前界面中文正常；这些是专用手动 Bottle 的实测，不等于新版 ZaH 安装全链已在该QQ上重跑。本轮保持当前QQ和卸载窗口，不接管该环境。新版自动字体准备、参数迁移及Linux原生入口接线依赖隔离回归；Linux新GUI工具与第三方安装需要另行真实验收。公共Bottles CLI不能指定任意工作目录，目前只支持EXE父目录；Linux没有desktop入口的普通deb尚无任意容器EXE浏览退路。不要将这些缺口称为已完成兼容性。


2026-11-02 的当前代码审查、实际修复及按用户流程区分的未完成项见 [质量审查](QUALITY_AUDIT_2026-11-02.md)。其中 Windows 批量导出、任务取消/重试、空目录备份和未知安装的停止范围已修复；永久数据删除与部分故障恢复仍不是完整闭环。

更新：2026-11-01。此文区分“开发实现和隔离回归可供复查”“可制作开发预览包”“实际应用已验证可用”。目前没有生成分发包、安装器、签名或宿主安装。

## 当前三页重构状态（2026-11-01）

用户已将产品收敛为应用库、管理、迁移。三页源码和安装卡片闭环已改，任务／归档／配方页面移除，操作进度与错误就地显示。新增依赖检查与明确确认后的基础包安装、应用平台限制、同应用同版本拒绝覆盖、同计划跨请求键防重复执行。

**尚不能称为功能全部完成或稳定安装版就绪。** 迁移合并此前被审批拒绝；用户明确确认稳定 ID 匹配规则后，本轮写入已获准并完成。相同身份／平台／架构／版本跳过，不覆盖已有应用／版本记录或私有数据；同名不同身份不合并。指定目录恢复已接通，只创建新的私有子目录，并持久验证目录设备／inode 身份。无实际用户数据迁移。

基础依赖安装已实现确认计划、原生确认、系统授权、持久任务、有界脱敏日志及失败重试。基础 APT 计划当前支持 Ubuntu 20.04/22.04/24.04、Debian 12/13 的现有 APT 源，固定模拟得到的包版本；拒绝升级、删除或未完成的已有包配置事务，不自动更新索引或添加源。仅按缺失命令准备 docker.io/distrobox 或 flatpak；Bottles、runner、Docker 权限和服务设置仍需单独处理。包安装脚本可能启动服务／创建系统用户，明确在确认窗口披露，不能称为无系统副作用。本轮未运行 pkexec 或安装系统依赖，授权取消等仅模拟验证。APT 来源是用户系统已配置信任的软件源，不能保证任意用户源都是官方源。

**受支持范围内的自动环境恢复已实现，Windows 同机自制 GUI 恢复已实测，跨机器尚未验证。** 完整包导入后，界面逐个提交 `environment.rebuild`；成功项变为 ready/stopped，失败项保留数据与中间身份，卡片提供“恢复环境”。Linux 校验 tagless 镜像、检查本地 Docker classic 存储空间，使用新名称、独立 HOME 和恢复 nonce 创建 Distrobox；经实际 UID/HOME/挂载/入口验证后精确停止。不重跑安装器，不修改 passwd 或初始化标记来强行兼容。原快照无法在新用户环境通过验证时明确失败，尚未实现任意跨 UID/用户名转换。

Bottle 恢复通过公开 `new/edit/add` CLI 创建专属目标，要求原 runner 已安装、相同用户名、可移植组件配置及 direct Wine。新包带摘要覆盖的 `restore.json`；安全暂存完整前缀文件，保留新目标 `bottle.yml`，不把源私有数据库直接注册到目标。以设备/inode 和非覆盖重命名发布，重建标准盘符，校验 EXE 摘要/PE、入口登记与精确 Wine 停止证据。源配置自定义环境/DLL override、组件版本不符、旧包缺少 profile、外部链接或跨用户名时拒绝自动恢复。不是通用 Wine 前缀转换器。

所选导入目录保存归档、专属 HOME 及恢复证据；Docker 镜像/层仍在其本机 data-root，Bottle 活跃前缀仍在 Bottles 默认目录。Bottle 新建的原始空前缀保留在同一私有数据父目录 `.zah-original-<attempt>`，用于失败恢复证据；尚无自动清理这份基线的流程，存在额外磁盘占用，不能说所有数据只落在所选目录。创建命令结束与身份保存之间若硬崩溃，未知目标不自动接管；需要人工核查。未完成登记的恢复资源会让全部停止/退出报告失败，不能静默称全停。

### Z1 Ubuntu 20.04 的具体阻碍

只读核实 Z1 为 Ubuntu 20.04.6，未安装 Flatpak，APT 候选为 `1.6.5-0ubuntu0.5`。Linux 基础命令检查/现有 Docker+Distrobox 不受这一 Windows 依赖问题影响；20.04 现有源能提供的缺失 Linux 基础包可生成安装计划，没有候选包时明确报错，不加源。

当前 [Bottles Flathub 清单](https://github.com/flathub/com.usebottles.bottles/blob/master/com.usebottles.bottles.yml) 使用 `per-app-dev-shm`；[Flatpak 官方元数据说明](https://github.com/flatpak/flatpak/blob/main/doc/flatpak-metadata.xml) 标明它从 1.12.0 提供。因此安装器拒绝小于这个功能下限的 Flatpak 候选；该下限不等于全部 Bottles/runtime 兼容保证。[上游 Flatpak PPA](https://github.com/flatpak/ppa-flatpak/blob/ppa/readme/README.md) 已声明不再更新 Ubuntu 20.04 及更旧版本，并有已知漏洞，不能把旧 PPA 作为正常解决方案。

现已在 Z1 构建固定私有 GLib 2.88.4 + OSTree 2026.5 + Flatpak 1.18.4 + Bubblewrap 0.12.0 + D-Bus proxy 0.1.9；版本、30 项上游路径/权限测试及无网络沙箱自检通过，不再以更换系统作为解决方案。用户随后授权的 Bottles/runner 安装与双 GUI 验收也已完成。固定用户目录重建及普通开发启动接入已验证，系统包和宿主服务未更改。这里只证明所列固定组合，不是全部 Windows 软件兼容。具体产物和限制见 [FOCAL_RUNTIME](compatibility/FOCAL_RUNTIME.md)。

Windows 受管 Bottle 精确删除、冷导出及同机恢复已实测；专属 HOME 永久清理仍因 proc 访问检查失败而保留数据。先前 Linux 实环境验收是旧安装路径证据，不等于新版全部流程已在第三方应用上通过。

### 本轮验证记录

- 修改前完整源码备份：`/home/zzh/Documents/Codex/2026-11-01/task/windows-product-20261102T171804Z/`。
- `go test ./...` 通过；最后修改后重跑受影响 backend/service/installmedia/recipe 通过，相关 `go vet` 通过。没有新增压力测试或本轮全仓 race 重跑。
- 桌面41项、前端55项通过，TypeScript和隔离Go/Vite构建通过。真实manager与无头Electron现有联调通过；截图是隔离夹具，未安装真实应用，运行按钮禁用不代表真实应用验收。
- 新manager对官方 `QQMusic_YQQWinPCDL.exe` 实际静态导入：`architecture=386`、保留原名、DPI192记录成功，实例保持unprepared，未创建Bottle或执行安装包。
- 构建与日志：`/tmp/zah-product-check-20261102/`；精确变更清单 `changes.json`。开发版本未覆盖用户活动 `bin/desktop/dist/frontend/dist`；无Git写操作、安装包制作或宿主服务变更。
- 原生卸载正常移除EXE后仍可用固定prefix/runner/namespace证据停止该环境；缺EXE不再阻断停止，但不会因此允许启动。已用隔离夹具验证，未替用户卸载QQ。

## 产品模块与数据流

| 模块 | 功能和原理 | 带来的作用 |
| --- | --- | --- |
| 应用库 React | 每应用选择版本，同一按钮运行/停止；运行期固定版本，不同应用可并行。以持久记录及任务事件刷新界面 | 普通用户不必管理进程、容器 ID 或多配置实例；不凭窗口消失判断已停止 |
| Electron 桥接 | 标准窗口与系统选择器；隔离 renderer；有限 IPC、文件选择授权、本地资源协议；连接独立 Go manager | 网页界面不持有任意宿主文件或 shell 权限，界面资源随包提供 |
| Go 服务与任务 | CLI/manager/worker 共用一个程序；计划、幂等提交、持久任务、锁和事件 | 丢失应答可重试原请求；界面断开不盲目重放安装；不同进程可共享同一真实状态 |
| 应用目录与 SQLite | 应用→版本→内部安装实例→专属环境；保存引用、身份、状态、审计 | 多版本可安装但同应用最多一个活动版本；名称不会冒充资源身份 |
| 材料与图标 | 安装材料独立复制、摘要寻址；PNG 规范化并按版本引用；有限格式静态图标发现 | 原始文件移走不影响归档；去重、引用保护及版本图标保持一致 |
| Linux 后端 | Docker+Distrobox；专属 HOME；完整容器身份复查；启动握手和精确停止 | 只管理登记且身份匹配的环境，显式停止不覆盖其他容器 |
| Windows 后端 | EXE/ZIP 引导准备专属 Bottle；原生安装器由用户操作，确认后保存入口；普通 runner、direct Wine；受控启动树排空后精确 WINEPREFIX kill/wait | 阻塞启动器仍能请求停止；延迟启动不能在停止之后重新进入；不使用全局 Wine kill |
| 备份迁移与加密 | 校验 `.zahbundle`、材料去重、冷环境包、显式共享内容、age；新目录准备后事务登记 | 区分材料恢复和可运行环境恢复；不覆盖原应用数据；失败回滚普通新资源 |
| 宿主集成与诊断 | 命令与运行时只读检查；固定 APT 包计划、原生确认、系统授权及日志 | 检查与安装分离；软件安装脚本的系统影响先说明，不隐式配置网络、权限或软件源 |

典型数据流：界面操作 → preload/Electron 校验 → Unix socket 长度前缀 JSON-RPC → Go service → SQLite 中的 Plan/Job → 独立 worker → 材料库或后端适配器 → 复核结果持久化 → 事件使界面重新读取状态。像素通过已登记 release 的小型 PNG data URL 返回，不开放任意宿主路径。

运行与停止是两种任务。Linux 确认完整受管容器身份后停止；Windows 先回收该次启动自己的进程树，再对已固定前缀和 runner 执行停止。全部停止只冻结 ZaH 登记的目标；ZaH 主窗口关闭先确认全部停止，失败留窗。外部应用 X 不自动推导为停止，用户可直接点击卡片停止环境。

## 支持矩阵与真实缺口

| 项目 | 当前实现/隔离验证 | 实环境状态及打包影响 |
| --- | --- | --- |
| 宿主 | Linux x86_64、普通用户；静态 Go；Electron sandbox/contextIsolation | Ubuntu 20.04.6 上无头联调通过；可见窗口、中文输入、原生对话框尚未验收。不能从无头结果推断所有 Linux 桌面支持 |
| Linux 应用 | 固定 digest 镜像；AppImage/deb；本机 Docker classic 存储；Distrobox 1.8.2.5 参数只读核查 | 已获Z1许可并以三个专用Ubuntu22.04容器完成自制deb/X11安装/运行/保存/版本切换/停止闭环；第三方应用和AppImage仍待指定材料实测，见REAL_ENV_RESULTS |
| Windows 应用 | 普通 Bottles 管理 runner，sandbox=false，显式 winebridge=false，无自定义环境/脚本/启动包装；受管进程树和精确前缀控制 | Ubuntu20.04 私有 Flatpak1.18.4 / Bottles67.5 / Soda11.0-10：两个自制 Win64 GUI 的安装器、保存入口、并行、单停/全停、备份恢复重新运行已实测；不等于任意 Windows 软件兼容 |
| 中断恢复 | manager/worker 隔离恢复和提交幂等；未知启动保留租约、防止重复启动 | 硬崩溃后的未知外部启动没有自动调和 API，可能需要人工检查，不能冒充自动修复。属于已知产品恢复能力限制 |
| 图标 | 本地 PNG；有限 ZIP/TAR/TAR.GZ `.desktop`+PNG 静态提取；失败 fallback | 不支持 AppImage/deb/EXE/ICO/SVG 自动提取，不阻断运行 |
| 迁移 | 目录、单实例和工作台冷备份；稳定 ID 合并、新 ID/目录恢复、Linux/Bottle 自动重建、失败重试 | 尚未跨机器/用户名验证；不保留 ACL/xattr/原 UID/GID/所有特殊文件语义；不声明 OfflineReady |
| 界面离线资源 | JS/CSS/Lucide 和资源协议均本地，无 CDN | 不代表系统后端、镜像、runner、应用授权和安装材料已离线备齐 |

Linux 基础闭环和固定 Ubuntu20.04 Windows 双应用/同机恢复闭环已有实证。私有依赖现已在长期目录重建并接入开发启动；源码/部署/组件摘要及已取得许可证原文也已保存。仍不能按“全部完成的稳定安装版”交付：真实私有数据永久删除被同用户不可读 `/proc` 进程阻止，需确认可恢复清理语义；正式发行的可重定位依赖、部分组件分发许可/源码义务、第三方应用、原生桌面交互和跨机器恢复仍未完成。按用户要求未制作安装包。详见 [REAL_ENV_RESULTS](REAL_ENV_RESULTS.md)、[私有数据清理诊断](compatibility/PRIVATE_HOME_CLEANUP.md)及[许可清单](licenses/FOCAL_NOTICES.md)。

## 下一步真实验收边界

1. 自制无网络Linux应用的三容器验收已完成并精确清理。用户正在准备第三方安装包；收到明确路径、应用名、版本及执行范围后再检查并验收，不扫描Downloads、不借用现用SakuraCat或生产容器。
2. 已获授权并完成私有 Flatpak/Bottles 双环境真实验证；后续解决私有 HOME 清理失败及固定依赖正式接入，不扩大为宿主系统安装/代理/权限变更许可。延迟启动、失败重试等故障分支以隔离模拟覆盖，不破坏生产环境制造故障。
3. 用户在桌面前完成 ZaH 原生确认取消/成功/失败、文件选择器、中文输入、最小化/最大化检查；只操作测试环境。Windows 标注实际验证的固定版本范围，不能隐藏第三方软件尚未验证的限制。
4. 完整迁移承诺需要第二个隔离账户/机器验证 HOME/用户名变化、绑定及真实应用数据读写。单机临时目录测试无法代替。

## 打包阶段本身要做的事

这些属于用户明确保留的下一阶段，当前不执行：确定目标发行版与包格式；添加 Forge 组装配置；确认 Go/renderer/preload 资源路径及权限；注入真实版本；安装/卸载/升级与数据保留验证；收集第三方许可证和 SBOM；决定项目自身许可及分发条款；签名/发布（若需要）。`release/` 仍仅预留，不能宣称已有安装器。

Node 直接依赖和 `package-lock.json` 固定版本，Go 依赖由 `go.mod/go.sum` 固定。Lucide 许可在 `docs/licenses/LUCIDE.txt`；Electron 运行时附带 LICENSE 与 Chromium notices，最终随包保留清单尚未制作。项目自身许可由所有者决定，不能由代理替选。2026-11-01 的 `npm audit --package-lock-only --ignore-scripts --json` 返回 0 条已知漏洞；这只表示当时注册表结果，不是完整安全认证。

开发入口：`npm run dev`；自动找到本机已安装 Go 和固定私有依赖，构建后打开窗口，使用用户级 ZaH 数据目录。`npm start` 使用已有构建，`npm run check:runtime` 只检查配置。已有 manager 活动时拒绝覆盖构建，不终止它。自动测试使用独立 XDG；开发构建不生成安装包。最终回归证据见 [DEVELOPMENT_VALIDATION](DEVELOPMENT_VALIDATION.md)及 [FOCAL_RUNTIME](compatibility/FOCAL_RUNTIME.md)。

具体可批准范围及本机镜像/权限盘点见 [REAL_ENV_VALIDATION_PLAN](REAL_ENV_VALIDATION_PLAN.md)。注意：当前Distrobox创建默认使用privileged、host namespaces与宿主根挂载，自定义HOME不是安全沙箱。依赖/许可静态准备见 [DEPENDENCIES](licenses/DEPENDENCIES.md)；已有源码SBOM，不是最终发行SBOM。


2026-11-01 默认启动故障已修复并增加真实默认 HOME/0775 state 父目录回归；来源是安全目录检查，非构建失败。安装引导已有模拟验证，随后固定 Windows GUI/runner 的真实闭环也已完成。源码改后重启时会检查桌面契约并提示等待旧 manager 空闲退出。具体安装步骤在 README，格式、限额、部分失败恢复和真实未测项在兼容说明。


## 2026-11-02 新版 Windows 图形流程实测

使用固定私有运行时、一个全新 Bottle 和独立认证 Xephyr 显示，已实际完成自制安装器准备/图形安装、原生文件选择器浏览 C 盘、显式确认并保存入口、卡片启动、单停、再次启动、确认全部停止。最终实例 ready、环境 stopped；当前用户已登录 QQ 保持运行。192 DPI 和内置 Noto CJK 的微软雅黑中英文/MS Shell Dlg 映射已在新环境生效；自制 I386 程序的简繁中文、日文、韩文及英文窗口已截图确认。

ZIP 验证复用了同一 Bottle：调用生产 Inspect/ExtractZIP 保留 EXE、资源子目录和 README，再通过真实文件选择器保存入口、运行并确认程序读取资源。为不新增第二个环境，本次没有把 ZIP 重新走一遍新版本环境创建；不能将此项表述为全套 ZIP UI 导入已实测。后续补验已实际点击同一 QA 卡片加号，使用原生文件选择器导入自制 I386 EXE，观察到读取进度、完成状态及自动打开安装新版本界面；x86 标识和本地 runner 检查正常。到开始安装按钮前返回，未新建第二个 Bottle。导入新增一个 succeeded 任务，原实例和环境数量不变。

完整日志、源码/二进制摘要和截图保留在 `/home/zzh/zah-real-win-sa23f6ld/logs/`。GUI 截图只含本次自制样例和隔离 ZaH，无用户账户内容。新 QA 数据保留未删除，测试窗口正常关闭、专用 Xephyr 已结束。Linux 新入口/维护窗口的真实验收仍待授权；第三方软件兼容性、特殊工作目录、跨机恢复等原有边界不因本次成功而消失。
