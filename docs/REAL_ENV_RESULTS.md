# Z1 实环境验收结果

本页前半记录 Linux 容器轮次，后半记录随后获授权的 Ubuntu 20.04 / Bottles 真实 Windows 验收；早期“Windows 未测”不代表后续轮次状态。

2026-11-01。用户明确批准现有Z1、最多三个专用容器、Distrobox已披露的较高权限与容器内官方依赖下载。只运行本项目生成的无网络测试程序；没有借用或安装未知第三方材料。本页是实测结果，自动回归见 [DEVELOPMENT_VALIDATION](DEVELOPMENT_VALIDATION.md)，产品模块与兼容矩阵见 [RELEASE_READINESS](RELEASE_READINESS.md)。

## 环境与证据

- 宿主Ubuntu20.04.6/x86_64/X11，Docker26.1.3/overlay2，Distrobox1.8.2.5。固定已有Ubuntu22.04镜像 `ubuntu@sha256:076e7bf82319619f28dd0aa9c02ab34b055ad37100395b4f49a2ffba04fb5e11`，没有pull新镜像。
- 夹具为自行编译C/Xlib程序，生成1.0、2.0两个无maintainer script的deb；入口 `/usr/bin/zah-qa-app`。仅显示测试窗口、写各自HOME心跳/保存内容、模拟关闭窗口但进程继续运行、接受自身退出命令；没有网络功能。
- QA Alpha1.0/2.0各一个容器，QA Beta1.0一个容器，总计恰好三个。数据用0700独立 `/tmp/zah-acceptance-i9_ayhyn`；原任务目录775，不降低私有目录校验或更改其权限。
- 永久证据：`/home/zzh/Documents/Codex/2026-11-01/task/zah-acceptance-evidence-20261101T104144Z`。包含夹具源码/deb、驱动脚本、任务结果、完整容器ID、目录快照、包清单、保存文件、清理回执及49个原始文件SHA256清单。`temporary-cleanup.json`为随后追加的清理回执。
- 真实运行截图：该证据目录 `results/real-linux-running.png`，已实际查看像素。Alpha选择2.0、Beta选择1.0，同时运行，版本选择锁定，按钮为“停止环境”；不是未安装的静态样例。

## 真实发现的问题与处理

1. 首次Distrobox enter会安装基础依赖，旧代码将id探测30秒超时，首个安装任务因此失败而容器继续初始化。修复所有受管enter的事务等待语义，不强杀正在执行的初始化。等待该容器初始化完毕，人工补装唯一自制deb后通过新显式 `instance.verify` 核实入口；原失败记录保留，没有改DB冒充原任务成功。第二、第三个容器从安装到入口核验完整自动通过。
2. Docker26不支持旧命令的 `--timeout`，单停第一次真实执行返回125。按本机help改用 `-t 10`。旧构建测试应用通过自身退出命令正常结束后，用新构建精确停止环境，随后完整重跑全部流程通过。
3. Linux停止失败后监督watcher会退出，后续请求无法及时完成；修复为保留控制、每秒限频重试且每次复核完整身份。增加首次失败/随后成功的模拟回归，不清除未知占用。真实停止参数修复后重跑成功；没有故意破坏宿主Docker来制造故障。
4. 同应用互斥原先显示context deadline exceeded，现以活动应用/冲突组占用说明拒绝，保留取消语义和实际互斥。

## 验收结果

| 项目 | 结果与实测口径 |
| --- | --- |
| 材料归档/版本记录 | 两份deb独立归档，Alpha两个版本、Beta一个版本；真实SQLite与任务 |
| 安装 | 第一份经显式人工修复核实，后两份新构建完整自动成功；不抹除第一次失败 |
| 普通用户GUI | 三环境均由Distrobox普通UID启动，Xlib在实际X11 display创建非零窗口ID；不抢焦点，不操作其他窗口 |
| 两应用并行 | Alpha与Beta心跳均推进；同App Alpha另一个版本启动被拒绝 |
| 保存与版本切换 | 1.0保存内容重启后保留；2.0独立HOME，保存2.0不覆盖1.0 |
| 外部窗口关闭语义 | 夹具主动销毁自己的窗口后心跳继续，ZaH保持运行/停止入口；未把窗口消失当作停止证明 |
| 精确单停 | 停止Alpha后Docker确认其停止，Beta心跳继续推进 |
| 全部停止 | 只冻结私有目录登记的三环境，逐个完整身份核实并证明停止 |
| 真实界面 | headless Electron真实React→preload→Go→Docker；点击单停、切Alpha2.0、运行、锁定版本；全停取消不新增任务，确认实际停止全部 |
| ZaH关闭协调 | 生产CloseCoordinator+真实Go后端，注入confirm/quit回调：取消不提交，提交前模拟断连不允许退出，重试确认全部停止后才允许退出；未操作原生关闭对话框 |
| 幂等与竞态 | 两轮实际重复job.submit返回同一job；启动与stopAll交错，最终停止且延后观察没有重新启动；queued-before-start取消的确定性场景另由模拟测试覆盖 |
| 清理 | 通过instance.remove删除恰好三容器，均先确认停止及ID/名称/标签；镜像不删除，不global prune |

真实运行失败与修复后的通过结果同时留存。`results/lifecycle.jsonl`含最初失败前的阶段与最终重跑；`results/rerun-lifecycle.log`、`rerun-ui.log`、`close-real.json`、`race.log`是最终验收依据。

## 依赖与清理

没有安装宿主系统库、Flatpak、Bottles或新工具链。使用宿主现有gcc/libX11开发文件与dpkg-deb生成夹具。容器内由Distrobox从原有Ubuntu官方源初始化依赖，再安装自制deb；未添加新源。每个容器最终316个已安装包（含基础镜像已有包，不是新增数量）；完整包名/版本在 `results/packages-<instance>.status`。

代表版本：libx11-6 `2:1.7.5-1ubuntu0.4`、sudo `1.9.9-1ubuntu2.7`、bash `5.1-6ubuntu1.1`、curl `7.81.0-1ubuntu1.30`、locales `2.35-0ubuntu3.15`、xauth `1:1.1-1build2`。这些仅存在于测试容器，随本轮容器清理移除。

三容器完整ID前缀为 `af3719944eb7`、`ffc5145d473f`、`f71826bbec75`；所有删除用完整ID且经生产后端核验。最终Docker清单恢复为原有三个容器：sakuracat26仍Up，原先停止的sakuracat24和sakuracat仍停止。没有对它们执行exec/start/stop/rm，没有更改现用代理/网络/DNS/路由/防火墙或Docker服务。

证据备份校验后，已删除整个本轮临时数据目录；等待其manager自行空闲退出并核实没有残留验收manager/worker/夹具进程。没有待完成的依赖安装。未执行Git stage/commit/push/reset、制作安装器、安装ZaH、签名或发布。

## 可以推进到哪里

Linux基础运行闭环已具备进入开发预览包制作准备的证据，但仍按用户安排不制作安装包。用户正在准备真实第三方安装包，尚未提供路径；没有扫描Downloads或擅自安装。下一步收到材料后核实格式/来源/版本及明确运行范围，再验收实际功能。

仍未验证：SakuraCat或其他第三方应用业务行为、真实AppImage、原生文件/关闭对话框和中文输入、Wayland、Windows/Bottles、跨机器/用户名恢复、ACL/xattr等不支持语义、全新离线安装、发布性能预算。Windows基础依赖需要独立系统变更许可。不能把本轮无网络自制应用通过扩大成所有应用兼容或完整稳定安装版就绪。

## 随后完成的 Windows 实环境轮次（2026-11-01）

用户另行批准：私有用户级官方 Flathub/Bottles/runtime/runner 下载、两个专属测试 Bottle、自制无网络 Windows GUI、单停/全停与备份恢复。没有宿主 APT 安装、系统源/服务/驱动/安全权限变更，没有操作现用 SakuraCat 或其他生产容器。不是原生 Windows 宿主支持。

证据根目录：`/home/zzh/Documents/Codex/2026-11-01/task/windows-validation-20261101`。隔离 manager 数据及 socket：`/tmp/zah-windows-validation-20261101`。只更新证据目录的静态 Go 测试二进制，没有覆盖用户的 `bin/zah`、`desktop/dist`、`frontend/dist` 或关闭用户窗口。

固定组合：私有 Flatpak **1.18.4**、Bottles **67.5**、GNOME runtime **50**、Soda **11.0-10 / Wine 11.0**。Bottles/GNOME 的实际 commit、runner SHA256 及宿主库边界见 [FOCAL_RUNTIME](compatibility/FOCAL_RUNTIME.md)。官方 Flathub GPG 验证保持开启；`runner-lock.json`、`component-lock.json` 记录 runner、Mono 11.2.0、Gecko 2.47.4、图形组件和字体下载摘要/来源。11 份实际缓存组件通过上游摘要核对；WineBridge 由应用提供，没有冒称下载了缺失的独立归档。

测试程序是 Go 交叉编译的 Win64/Win32 窗口，只有编辑框、保存按钮、心跳和本地文件；没有网络功能。第二程序包含自制安装器，测试点击的只是自己的安装按钮，没有代用户接受第三方 EULA。截图已实际核看：`screenshots/A-initial.png`、`Installer.png`、`B.png`、`A-restored.png`。此轮通过真实 Go RPC 驱动后端及真实 X11 窗口，没有把 React 静态截图当成后端验收。

| 项目 | 真实结果与证据 |
| --- | --- |
| Windows 便携 EXE | 归档、创建 Bottle、注册入口、真实 GUI 与持续心跳成功；`logs/running-A.json` |
| 安装器 GUI | 显示安装窗口，自制按钮写入 `C:\ZaHInstalled\app.exe`，安装器结束；显式停止环境后确认安装完成、保存新入口；`logs/installer-exit.json`、`installer-stopped.json`、`finish-B.json` |
| 安装后再次启动 | 从保存的新入口运行 B，窗口和心跳通过；`logs/running-B.json` |
| 两应用并行、精确单停 | A/B 同时运行；停止 A 后其心跳停止而 B 持续更新；`logs/single-stop-isolation.json` |
| 全部停止 | 两个明确登记目标，`stopped=2, failed=0`，之后心跳均不再推进；`logs/stop-all.json` |
| 保存与重启 | A 重启后保存内容保留；夹具第一次定时事件也会保存，因此这不是“人工点击保存按钮”的专门验收 |
| 冷备份 | 完整 `.zahbundle`（tar+gzip 格式，不是假称 ZIP），559222415 字节；SHA256 `cbeb9bd77cc36d34c9d5b84fe26b5549e3246617b7cdd1207cf1d0de9cc910fd`；另用独立读取核对 EXE 和保存文件；`logs/export-A.json`、`backup-payload-proof.json` |
| 稳定身份合并与指定目录恢复 | 移除测试 A 的运行环境、保留其私有 HOME，删除原版本登记；按稳定身份向原应用合并缺少版本，恢复到指定新私有子目录；`logs/restore-A.json` |
| 运行环境重建及重新运行 | 先移除原 A，恢复副本使用释放的测试名额，始终最多两个登记的物理 Bottle；恢复前后 EXE 摘要和保存内容相同，真实重建后重新显示窗口/心跳，并精确停止；`logs/rebuild-A.json`、`restore-before-launch.json`、`restore-running-A.json`、`restore-stop-A.json` |

### 本轮修复与保留的失败证据

1. **临时 inode 误当永久身份**：Flatpak 会在所有实例退出后回收 per-app `/tmp`，原代码误报 Bottle/runner 被替换。对已核实的 Flatpak 1.18+ 共享机制，持久身份固定 sandbox policy、安装/runtime、宿主 runtime 根、runner 摘要和 prefix inode；临时目录 inode 在操作期间仍核对。未知/旧机制保留严格比对。新增空闲 GC、作用域改变、prefix/runner 替换、旧机制以及操作中替换回归，未关闭身份检查。
2. **已停止环境被误报停止失败**：Wine `wineserver -k` 没有目标时返回 1。现在只允许进入后续有界的同前缀 `-w` 验证，不能凭 1 宣称成功；等待失败仍失败。真实运行期间首先由原有 supervisor 回收本次启动自己的进程树，不使用全局 Wine kill。
3. **默认 Soda 用户别名阻止备份**：只支持 `drive_c/users/steamuser` 指向同级实际用户目录的单一相对链接。空目录也入包；恢复先验证全部普通文件与摘要，最后才建立别名。绝对路径、父级跳转、自环、链接链、缺失目标、普通文件目标、经别名写入均拒绝；宿主 HOME 和 dosdevices 外部目标不跟随打包。新增完整导出/验证/恢复和反例测试。
4. **指定恢复目录无法运行**：两个启动路径校验曾硬编码默认数据根；统一使用已有登记根的 inode/所有权校验。新增完整 Windows 运行/停止回归及根目录被替换的拒绝测试，实际恢复 GUI 已重跑通过。

验收脚本曾遗漏 `CGO_ENABLED=0`，被已有“监督器必须静态链接”检查正确拒绝；随后按项目构建脚本参数修正。另一次重启检查读取到旧心跳文件过早，修正为等待内容改变，未重复提交未知状态启动。这些是测试驱动问题，不是删安全检查解决。

### 未解决项与兼容边界

**私有 HOME 永久清理仍有真实阻碍**：`instance.data.remove` 在读取同 UID 的 `/proc/2166/cwd` 时被系统拒绝，当前代码因无法排除文件使用而安全失败。未跳过检查、未提权、未更改 ptrace/proc 权限，也未操作该进程。A 的受管 Bottle 已能正常删除；随后使用现有非破坏性的 `instance.forget` 保留 HOME 和审计记录，才继续版本恢复。本轮不是“删除功能全部通过”。需要在不降低数据安全的前提下完善清理策略/失败反馈，不能为通过验收修改宿主安全设置。

此轮只证明 Ubuntu 20.04.6/x86_64、当前内核/X11/驱动和固定组合上的两个简单 Win64 GUI。未证明任意 EXE/ZIP、32 位应用、3D/音视频/.NET、驱动/反作弊、Wayland、跨机器/用户名恢复或全离线安装兼容。字体等组件的分发许可、私有 Flatpak 的绝对路径重建/发行接入、宿主内核/显示/驱动及剩余原生库依赖仍须打包阶段逐项处理；不能把测试目录直接复制当成通用安装版。

### 最终回归与清理

最后生产补丁后：`go test -race ./...` 与 `go vet ./...` 通过；Electron/React TypeScript 检查通过，desktop **40/40**、frontend **48/48** 通过。desktop 编译输出位于 `/tmp/zah-windows-final-desktop-check`，没有覆盖用户现用构建；没有生成分发包。

原 A 已在恢复前通过 `instance.remove` 精确移除；恢复验收后，B 和恢复后的 A 也通过同一接口移除，专用 Bottles 目录中已无 `zah-*` 实例。manager 自行空闲退出；核对 PID/start identity 后仅关闭私有测试 D-Bus 和 helper。没有残留测试应用/manager/依赖安装；`logs/final-bottle-removal.json`、`final-process-cleanup.json` 保存回执。私有 HOME、恢复暂存副本、SQLite、备份和官方下载缓存保留用于审计/复现，不声称磁盘数据全部清空。宿主 `.var/app` 新增框架目录也保留，未做全局删除或卸载。

### 固定目录与普通开发启动收尾

私有原生依赖已在 `/home/zzh/.local/share/zah-focal-runtime-1.18.4/runtime` 重建，通过完整构建、30 项上游测试及沙箱自检。`/home/zzh/.local/share/zah-focal-desktop/profile.json` 绑定同一已验证 Bottles/GNOME 提交及 Soda 版本，仓库与可复用组件从已授权缓存复制；没有复制测试 Bottle、HOME 或恢复暂存。`npm run dev`/`npm start` 自动使用此配置，保留 Electron 桌面 D-Bus，仅 Bottles 子调用创建临时会话。

实际 `npm run check:runtime`、沙箱 Python 打印私有 XDG 路径以及 Bottles CLI 空列表通过。首次空目录缺失已修复；CLI 曾记录上游目录连接重置后仍返回 `{}`，不宣称全离线。新增入口回归加原构建器共 24 项通过；包括活动 manager 拒绝、配置篡改/坏链接不回退、测试数据不复制、临时会话清理和错误传播。未实际执行新的 `npm run dev`，避免覆盖用户构建；没有用新固定配置再创建 GUI/Bottle。原双 GUI 全流程证据属于前一验收配置。

复查本次私有 helper 无残留；未重试私有 HOME 永久删除。只读诊断、原地保留并解除登记的可恢复提案见 [PRIVATE_HOME_CLEANUP](compatibility/PRIVATE_HOME_CLEANUP.md)。新增依赖清单保留 1533 份上游原文及摘要，但最终分发许可/对应源码义务仍有明确缺项，见 [FOCAL_NOTICES](licenses/FOCAL_NOTICES.md)。本次没有系统包安装、Git 写操作、生产服务变更或安装包生成。


## 2026-11-03：EXE 直接打开与 Linux 图形安装修正

Windows 新界面移除“程序用途”：单个 EXE 从卡片加号选入后自动准备并打开，ZIP 只让用户选目录内的 EXE；以后运行的入口仍需用户图形选择并确认。旧 installer/portable 记录兼容保留，不从文件名或启动器退出推断用途/完成。

本轮隔离产品链实际使用自制无网络 fixture-b.exe：原生文件框选择→真实导入进度→专用 Bottle 准备→EXE 窗口自动出现→停止→原生文件框选择同一个便携 EXE 为最终入口→保存→卡片再次运行并出现窗口/新进程心跳→精确停止。QA 根 `/home/zzh/zah-real-win-sa23f6ld`，新增专属实例 `c861246546cb443594c663e4874a8435`，不涉及已登录 QQ。截图只来自认证 Xephyr :97。一次截图遇到未重绘区域，限测试 display 请求 expose 后看到控件；没有改宿主显示或权限，保留原始空白截图以便复查。

Linux 目标 DEB 不再经 apt-get 静默安装；初始化容器声明的工具后打开 gdebi-gtk，由用户作安装决定。新 Linux 配置声明 python3、libglib2.0-bin、gdebi、synaptic、thunar，仅容器内。已通过 backend/service 相关测试、前端 31 项、桌面启动/安全 22 项及隔离构建。Linux 原生工具/底层两次启动由独立 QA 验证；其旧非产品身份容器未冒充产品记录，当前新产品卡片完整链仍未实测，需要明确允许新建一个合法身份的专用测试容器。

边界：Windows 此轮真实链用自制 EXE；先前 32 位 CJK/ZIP 邻接资源真实结果仍按各自轮次记录。拖放桥接与同一导入路径有回归，未把原生 OS 拖放手势称为本轮实测。独立 CWD 与特殊图标字体、各第三方音视频/GPU 兼容仍需逐类验证，不承诺任意应用兼容。新的多字体/多语种图形验证另组进行。


## 2026-11-03：真实多字体 / DPI 验证与最小回退修正

先用已有专用 Bottle `zah-c861246546cb443594c663e4874a8435` 运行自制 32 位 Win32 GDI 窗口，随后才修改产品代码。本轮没有新建 Bottle、没有执行 QQ 安装器、没有播放音乐或访问账号数据；没有新增测试框架，只扩展已有正常安装测试。

连续过程与真实结果：

1. 192 DPI 原配置：中英文 Microsoft YaHei / 微软雅黑 / MS Shell Dlg 与 Noto 粗体可读；Arial、Tahoma、SimSun 和故意不存在的字体名请求出现 CJK 方框。`01-fonts-192-before.png`。
2. 单独追加 Arial/Tahoma 的 SystemLink 未解决，后续启动观察到链重建；不能把注册表写入成功算显示成功。Wine 上游有默认链重建逻辑，但本机 Soda 的具体触发来源未完全归因。
3. 临时三项替代能修 CJK，但额外文字覆盖退步。此方案没有直接发布。`03-fonts-192-aliases.png` 保留失败项。
4. 最终 288 DPI：撤销 Arial/Tahoma 替代，保留原生拉丁字体，仅添加 SimSun → Noto Sans CJK SC，并为 Noto 增加 DejaVu Sans 的 SystemLink。常见中英文字体请求、简繁/日/韩混排、未知字体回退及粗体实际显示正常。`05-native-latin-top.png` 与 `05-native-latin-bottom.png` 是同一窗口连续上下部分。希腊和阿拉伯字形可见；这不代表阿拉伯整形/双向排版已验证。印度文字、泰文仍有方框，明确未通过；U+E001 是刻意无专用图标字体的探针，不应由通用字体冒充图标。

`GetGlyphIndicesW` 的 direct missing 仅测当前选中字库，Arial/Tahoma 的数值仍是 20，而屏幕 CJK 已通过回退显示。不能用这个数字代替屏幕效果，也不能把本次 GDI 结果推广到所有 DirectWrite/Chromium/游戏渲染器。实际 ANSI codepage 为 1252，并非推测的 UTF-8。

整合：`internal/backend/bottles_fonts.go` 只对新建的受管 Bottle 准备上述字体；没有批量替换字体、改宿主字体或改已有 QQ 配置。Noto 20,332,392 字节（OFL-1.1），新增原版 DejaVu Sans 757,076 字节（Bitstream Vera / DejaVu 修改公有领域），来源、SHA256 和原始许可保留于 `internal/backend/fonts/`。字体总原始体积 21,089,468 字节。保留应用自带图标字体；字体供应仍不等于所有语言覆盖。

验证：`go test ./internal/backend ./internal/service` 通过，正常 EXE/ZIP 准备回归同时确认字体/许可证落盘、SimSun 和 Noto 回退声明、没有 Arial/Tahoma/QQMusicUI 全局替代；Go 构建输出 `/tmp/zah-font-fallback-20261103` 成功，未覆盖活动 bin/dist。真实显示来自先行 QA 配置，最后源码整合用上述相关测试与编译复核；没有把新二进制再装入用户应用或重复新建测试环境。

清理：QA 原 bottle.yml / user.reg / system.reg 已从本轮备份恢复，custom_dpi 回到 96，原已保存 `drive_c/zah-material/fixture-b.exe` 入口保留，重新核验身份并精确停止成功。额外字体文件作为未注册 QA 资产保留；manager 已结束，认证 Xephyr :97 已正常关闭，本轮启动的四个相关 PID 均不存在。没有停止生产代理、改网络/DNS/路由/权限、改全局显示、Git 写操作或打包。

源码修改前完整备份：`/home/zzh/Documents/Codex/2026-11-01/task/font-fallback-before-20261103T0755Z/source-before.tar.gz`。原始窗口、coverage JSON、失败尝试、恢复结果和 Library 图片身份均在 `/home/zzh/zah-real-win-sa23f6ld/font-qa/`。

真实 QQ 证据另组：复核此前音频测试的 `wasapi-restoration-persisted.png`，该设置页中文标签清楚。它是已有真实 QQ 截图，不是本轮新默认字体的 QQ 回归；部分原生下拉框此前仍报告缺字，本轮没有重新打开 QQ 去验证。已安装的旧环境不会因重编译自动获得新字体配置。

参考：[Microsoft 字体回退与链接](https://learn.microsoft.com/en-us/globalization/fonts-layout/fonts)、[GetGlyphIndicesW](https://learn.microsoft.com/en-us/windows/win32/api/wingdi/nf-wingdi-getglyphindicesw)、[Wine 11 font.c](https://raw.githubusercontent.com/wine-mirror/wine/wine-11.0/dlls/win32u/font.c)、[DejaVu 许可](https://dejavu-fonts.github.io/License.html)。
