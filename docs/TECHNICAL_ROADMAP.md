# ZaH：Linux 应用运行与迁移工作台技术规格

版本：2026-10-31。本文规定待实现产品的架构、行为与验收条件。历史 SakuraCat 原型不代表这些功能已实现；支持的精确版本与性能结果以实测记录为准。

## 1. 目标与范围

面向正常工作的 x86_64 Linux 桌面，管理较新 Linux 应用和 Windows 应用，使其通过成熟运行环境使用宿主显示、音频、文件和网络能力。

核心功能：

- 保存用户导入的安装材料及来源记录。
- 按应用、平台、版本组织运行实例，支持快速启动、并存和清理。
- 准备运行环境、安装应用、诊断失败并验证实际功能。
- 共享宿主文件，保留用户数据。
- 导出单个实例或整个工作台，在另一台兼容电脑导入恢复。

首版不包含移动应用、Windows 虚拟机、跨 CPU 转译、通用游戏优化、任意应用自动更新和磁盘配额。Ubuntu 20.04 为重点旧宿主验证对象，较新 Ubuntu 为对照；只有通过验证的组合才能列入支持范围。

交付优先级为：无需用户另装界面运行时、数据与兼容可靠、快速响应、控制整体体积。Docker/Bottles 等运行后端按需准备，不能成为打开管理界面的前提。

## 2. 技术栈与运行原理

### 2.1 管理工具

- 桌面：Electron；提供随包 Chromium/Node、窗口与系统对话框。
- 前端：React、TypeScript、Vite、Tailwind、shadcn/ui；React Router 管导航，TanStack Query 管异步状态。
- 核心：Go；负责 CLI、任务、材料、运行后端和数据管理。
- 存储：SQLite，使用 database/sql + modernc.org/sqlite，无 CGo 构建路径。
- 分发：Electron Forge 组装版本化安装目录；安装或手工解包一次，日常启动不解压。

用户无需预装浏览器、Python、Node、Go 或 WebKitGTK。Electron 负责一致的界面运行环境，Go 避免另行打包解释器；不在 Node 与 Go 中重复实现业务。[Electron](https://www.electronjs.org/docs/latest/)、[SQLite 驱动](https://pkg.go.dev/modernc.org/sqlite)

Go 构建目标为 linux/amd64、GOAMD64=v1、CGO_ENABLED=0。工具链和依赖固定版本，核对最终 ELF 动态依赖，在目标桌面验证。Go 参数不能替代 Electron 和外部后端的兼容测试。

### 2.2 两个应用运行后端

**Linux：Distrobox + Docker。** 较新发行版的用户空间提供库依赖；程序连接宿主已有显示、音频等服务。容器共享宿主内核，不解决宿主缺少的内核特性或驱动能力。普通应用和 VPN 使用不同配方及权限。[Distrobox](https://distrobox.it/)

**Windows：Flatpak 发行的 Bottles + 经验证的 Wine runner。** Wine 实现 Windows 接口，Bottles 管理前缀、运行器和程序；工具优先调用受支持接口，不修改上游私有数据库。[Wine](https://www.winehq.org/about)、[Bottles CLI](https://docs.usebottles.com/advanced/cli)

例如 QQ 音乐可以通过 Wine 音频后端连接宿主 PulseAudio/pipewire-pulse，再使用宿主驱动和播放设备。音视频数据不经过 Go 或 Electron。声音输出、媒体键、独占输出、在线授权分别验证，不能用“能出声”证明全部兼容。[PipeWire](https://docs.pipewire.org/page_man_pipewire-pulse_1.html)

先解析安装包类型、架构与可得元数据，再结合宿主事实选后端。诊断输出包括证据、判断、建议和限制；未知信息保持未知。存在更合适的官方原生版本时可提示，不私自替换用户指定版本。

## 3. 界面与数据模型

### 3.1 应用库

四个主要区域：应用库、添加应用、运行环境、任务详情。材料管理与迁移通过应用库工具栏进入。

应用库按应用折叠，展开显示平台、版本、配置实例、运行状态、验证状态、上次使用和存储占用。示例标签不代表已验证版本：

    SakuraCat
      Linux · 版本 A · 日常       启动 / 详情 / 导出 / 删除
      Linux · 版本 B · 测试       启动 / 详情 / 导出 / 删除
      Windows · 版本 C           未验证 / 准备环境
    QQ 音乐
      Windows · 版本 D · 日常     启动 / 文件位置 / 详情

点击具体实例启动已登记入口，不重新执行安装器。收藏、默认版本及桌面快捷方式关联稳定实例 ID，不通过 latest 标签暗中切换版本。

### 3.2 实体与身份

- AppFamily：应用身份；通过配方或用户确认归组，不只匹配文件名。
- AppRelease：应用发布记录，含平台、版本标签、架构和安装材料集合。同版本标签不同材料可为不同发布记录。
- AppInstance：可运行副本，关联 Release、Environment、配方修订、入口、私有数据与验证结果；内部可保留历史安装副本，但当前产品不面向同应用多开；普通首页每应用一个版本选择与运行入口。
- Artifact：按 SHA256 标识的原始安装材料，关联来源记录与引用。
- Environment：容器/bottle、后端与运行时信息、所有权、实际状态。
- FileBinding：宿主路径、应用可见路径、访问方式、权限范围和验证结果。
- Plan/Job/Step：不可变操作计划、任务与步骤执行证据。
- CapabilityResult：某宿主和具体版本组合下的能力验证结果。
- ResourceRef：材料、镜像、runner、数据与实例之间的引用。
- MigrationManifest：迁移格式、资源清单、依赖与重绑定要求。

版本标签允许未知或用户备注，不作为唯一键。记录 original_artifact、observed_installed_version 和漂移状态；应用自行更新后不能把旧安装包说成当前版本的精确来源。

默认每个实例独立可写环境和配置；镜像、runner、同摘要材料可共享。共享已有音乐库不等于共享业务数据库。同一 AppFamily 同时最多运行一个版本，不同应用可并行。端口和其他跨应用限制仍可设置额外冲突组；安装隔离与运行并发是独立规则。

## 4. 进程、通信与模块边界

### 4.1 职责分配

React renderer 只显示状态并提交意图。Electron main 负责窗口、文件/目录选择、系统打开操作及 Go 连接；preload 暴露有限类型化操作，不暴露任意 shell、文件读写或完整 ipcRenderer。

一个用户一个 Go manager，提供服务、数据库、任务队列和事件。耗时修改由同一 Go 二进制的内部 worker 子命令执行，按需启动。Electron 自身的 renderer/GPU 等进程计入产品资源占用。

Domain 不依赖 UI 或具体后端。Service 组织用例，Backend 隔离上游接口，Infrastructure 提供进程、锁、路径、授权与存储。SQLite 只由 Go 访问。

### 4.2 通信契约

静态页面通过 zah://app/ 本地协议读取随包资源，只允许规定目录和类型，拒绝路径穿越。生产环境不启动 Vite 服务，不加载远程业务页面。

调用链为 React → preload → Electron IPC → main → Go Unix socket；CLI 使用同一个 socket，不监听 TCP 端口。[Electron IPC](https://www.electronjs.org/docs/latest/tutorial/ipc)

socket 位于用户私有运行目录（0700），socket 权限限制本用户并校验对端身份。协议采用 JSON-RPC 2.0 请求/响应/通知语义，使用长度前缀分帧，限定消息大小、请求并发数、超时与输出量。连接建立时握手检查协议兼容版本。

事件携带递增序号；重连按游标续读，历史不可用时返回快照。schemas 中的规范生成 Go/TS 类型，契约测试校验实现；renderer 不持有 socket 路径或运行授权材料。

### 4.3 生命周期与互斥

启动 Electron 与连接/启动 Go 并行进行，以 ready 消息确认服务可用，不使用固定 sleep。首页读取本地缓存，相关环境状态随后刷新。

用户正常关闭 ZaH 窗口时，先以与“全部停止”一致的风险提示确认；取消保持打开，确认后停止准确登记的受管环境，核实全部结果成功才退出 Electron，失败保留窗口并显示原因。连接异常断开本身不自动取消 worker。manager 在没有客户端、排队任务、执行任务或恢复事务时空闲退出；退出检查与新任务接收必须互斥。有任务时保留必要后端，重新打开后接续进度。

manager 使用单实例锁；worker 持有全局修改锁和环境锁。涉及多个环境的操作按稳定 ID 顺序加锁，避免死锁。CLI 启动同样遵守环境锁；manager 不存在时只能启动已登记实例，安装/更新仍进入统一任务队列。

进程身份使用 PID 与启动身份等证据共同判断，不能只看 PID。worker 日志独立写入文件，不依赖旧 manager 的输出管道。manager 崩溃恢复时先核实旧 worker，不重复执行任务。

本工具的锁不能阻止上游 GUI 或用户终端操作；修改前后检查上游状态与可用锁，冲突时要求停止其他操作。不能确保互斥时不自动重试有副作用的步骤。

## 5. 文件结构与扩展接口

### 5.1 统一命名

产品名称为 **ZaH**，用于窗口标题、菜单显示名和文档；文件系统及程序标识统一使用小写 **zah**：

- 项目目录和仓库名：zah；当前本机工程路径为 /home/zzh/zah。
- CLI 与 Go 核心程序：zah；源码入口为 cmd/zah/main.go。manager、worker 是该程序的内部子命令。
- 前端 package.json 的 name：zah；Go module 路径在仓库地址确定后采用对应的仓库路径，不预设账号或托管平台。
- 桌面入口：zah.desktop，显示名为 ZaH；静态资源协议为 zah://app/，仅用于应用内部资源，不注册为系统外部链接处理器。
- 配置、数据、状态、缓存分别置于对应 XDG 目录的 zah 子目录；用户运行目录中的 socket 为 $XDG_RUNTIME_DIR/zah/manager.sock。
- 受管容器和 bottle 的名称采用 zah-<instance-id>；应用名称和版本用于界面展示，不作为资源唯一身份。
- 普通迁移归档扩展名：.zahbundle；manifest 中的格式标识为 zah.bundle，schema_version 单独管理格式兼容性，不与产品版本绑定。

未设置 XDG 配置、数据、状态、缓存变量时，分别使用 ~/.config/zah、~/.local/share/zah、~/.local/state/zah、~/.cache/zah。运行目录需验证归属与权限；XDG_RUNTIME_DIR 不可用时由路径模块建立本用户独占的安全临时运行目录，所有入口使用同一解析规则，不使用可被其他用户预占的公共固定 socket。

工程结构与开发入口见 [README](../README.md)，当前实现及验收边界见 [模块与就绪清单](RELEASE_READINESS.md)。旧Python原型和过时教学资料保存在仓库外可恢复备份中；Git仍由用户管理，不将原型数据直接当作新版数据格式读取。

### 5.2 目标文件结构

以下为后续实现的目标布局；旧 Python 原型已移出，不属于新版源码树：

    zah/
    ├── go.mod / go.sum
    ├── package.json / package-lock.json
    ├── LICENSE / THIRD_PARTY_NOTICES.md / SECURITY.md
    ├── desktop/
    │   ├── main/                  窗口、对话框、Go 连接
    │   ├── preload/               有限类型化接口
    │   └── protocol/              本地资源协议
    ├── frontend/src/
    │   ├── app/                   路由、布局、查询缓存
    │   ├── features/              应用、安装、环境、任务、迁移
    │   ├── components/ui/          按需组件
    │   └── lib/                   类型化客户端
    ├── cmd/zah/main.go             CLI/manager/worker 入口
    ├── internal/
    │   ├── bootstrap/             组装与单实例
    │   ├── domain/                实体、状态、错误
    │   ├── service/               业务用例
    │   ├── catalog/               材料与资源引用
    │   ├── migration/             清单、导出、导入、重绑定
    │   ├── transport/             socket、RPC、事件
    │   ├── backend/               接口、Distrobox、Bottles
    │   ├── recipe/                配方与特殊适配
    │   ├── host/                  宿主探测、文件和能力集成
    │   ├── jobs/                  执行与恢复
    │   ├── process/               参数、超时、环境、进程身份
    │   ├── infra/                 路径、锁与授权
    │   └── storage/               SQLite、仓储、SQL 迁移
    ├── recipes/                   声明式应用配方
    ├── schemas/                   RPC、配方、迁移规范
    ├── tests/                     契约、集成、Electron E2E
    ├── scripts/                   构建、安装、验证
    ├── release/                   Forge、SBOM、签名配置
    └── docs/                      技术规格、部署、兼容报告

### 5.3 扩展接口

RuntimeBackend 接口包含 probe、assess、planInstall、executeStep、inspect、launch、stop、planRemove、describeSnapshot、exportSnapshot、planRestore。检查和计划无副作用，执行结果带资源身份与证据；能力字段明确哪些操作可自动完成、需交互或不支持。

ArtifactStore 提供 import、verify、references、planRemove。CatalogService 管理应用/版本/实例。MigrationService 提供 planExport、export、inspectArchive、planImport、import、verifyRestored，复用任务、锁、日志和授权，不增加常驻服务。

Go 接口与结构体组合实现可替换后端。普通软件增加 JSON 配方，特殊行为才增加 AppAdapter；配方包含匹配、依赖、环境、安装策略、入口发现、宿主能力和验证。禁止任意 shell 模板及安装包动态加载插件。

Bottles 程序列表不能保证发现全部手工安装结果。入口识别结合配方、上游登记与用户确认；EXE/MSI 分开处理。不支持单应用停止时如实返回，不结束整个 Bottles 或按名称批量杀进程。

包/脚本文档说明用途、执行位置与影响。Go 使用 GoDoc，TS 使用 TSDoc；原 Python 代码保持 :param: / :return: 风格。Go 单元测试放在包旁，跨模块验收放 tests；避免为未来功能创建空实现。

## 6. 安装材料、安装与任务恢复

### 6.1 持久归档

导入时将材料独立复制到 $XDG_DATA_HOME/zah/artifacts/sha256；流式计算摘要、验证副本、原子提交后才安装。禁止只保存原路径/软链接或可随原文件修改的硬链接。材料不是临时缓存，默认保留至用户明确删除。

同摘要去重，来源记录保留原文件名、原路径、导入时间、平台/架构、声明版本、可得来源和签名信息。未知来源保持未知。同名版本号但摘要不同不能覆盖。空间不足应停止或让用户选择位置，不退化为引用原文件。

检测阶段只解析包头/元数据，不执行安装器；解析限制时间、输出和解包规模。AppImage 自提取仍会运行包内代码，只能在确认后的安装阶段执行。[AppImage](https://docs.appimage.org/user-guide/run-appimages.html)

安装使用归档副本，记录配方、实际依赖、镜像/runner、计划、结果和验证证据。引导安装器的归档不保证其远程资源永久存在；界面区分“材料已保存”和“离线重装材料齐全”。

### 6.2 计划与状态

操作先生成不可变 Plan，包含材料摘要、配方修订、环境变化、权限、数据影响和执行前提。执行前重新核对条件；写操作使用幂等键防止双击重复提交。

Job 状态为 queued、running、waiting_user、cancel_requested、succeeded、failed、cancelled、interrupted。状态机显式校验合法转换；waiting_user 的安装器仍占用环境时继续持锁。应用/环境状态独立于任务状态。

安装器退出成功不代表安装完整。只有入口、文件和必要记录符合预期才登记就绪。崩溃后查询上游事实，确定已完成步骤再续做；不根据数据库中的 running 状态直接重装。

取消在安全边界生效；下载可中止，包数据库写入不得任意强杀。外部副作用不能依靠 SQLite 回滚，失败时仅撤销本次明确创建且无其他引用的资源，保留部分完成证据。

### 6.3 状态与存储

SQLite 使用 WAL、短事务、有限 busy timeout，各进程独立连接；外部命令执行期间不持写事务。表按第 3 章实体建立，另记录事件、引用和迁移任务；JSON 扩展字段带版本，关键查询字段独立存列。

受管数据与材料放 $XDG_DATA_HOME/zah，管理状态放 $XDG_STATE_HOME/zah，配置放 $XDG_CONFIG_HOME/zah，可重建缓存放 $XDG_CACHE_HOME/zah，锁/socket 放用户私有运行目录的 zah 子目录；默认路径按第 5.1 节解析。日志脱敏、限额轮转；追溯清单不随日志删除。

数据库记录与上游实际状态冲突时显示 missing/changed/unknown，不自动覆盖。managed/imported 所有权和创建证据决定是否能删除；导入已有资源默认只取消登记，完整删除另行确认。

## 7. 文件共享、多版本与清理

Linux 使用独立 HOME 和明确的宿主目录映射，GUI 以普通用户运行。Windows 同时配置 Wine 路径映射与 Bottles/Flatpak 访问权限。Electron 选择目录后，Go 仍验证路径和权限。[Bottles 目录访问](https://docs.usebottles.com/flatpak/expose-directories)

不设置固定容量，按实际使用占用宿主磁盘。安装前分别检查材料库、Docker 存储、Flatpak 数据等实际文件系统的剩余空间和临时峰值。普通内容读写不经过管理器代理。

基础能力包括读取、保存、下载、重命名、删除和重启后持久化。拖放、应用内打开文件夹、回收站和默认打开方式单独记录。工具自己的“在宿主打开目录”不等于修复第三方应用同名按钮。

共享权限说明实际范围：Distrobox 不是强沙箱；同目录可经其他路径访问；Flatpak 权限可能影响多个 bottle。只读映射不保证不存在其他可写入口。[Flatpak 权限](https://docs.flatpak.org/en/latest/sandbox-permissions.html)

目录失效或外置盘未挂载时提示，不在空挂载点继续写。共享文件的修改/删除真实作用于宿主；缓存加密或授权限制也不会因文件可见而消失。

导入新版默认新建实例，保留旧实例。原地自动更新记录漂移并使旧验证失效。私有配置不自动跨版本共享；必要迁移只能在应用退出后执行并保留恢复材料。

清理界面分别选择：移除实例、删除私有数据、删除安装材料、回收无引用的运行时。外部内容默认保留；私有目录内也可能有音乐、书籍、笔记和登录状态，不能按 cache 名称推断可删除。

删除先确认相关进程退出、资源身份及引用。共享层回收量无法精确计算时标为估算，不执行全局 prune，不追随外部软链接递归删除。材料保留时可留下“已归档、需重新安装”的版本条目。

## 8. 备份与跨机器迁移

### 8.1 用户流程与一致性

选择实例或整个工作台 → 确认包含/遗漏内容 → 停止写入者 → 导出归档 → 在目标机导入 → 检查条件并重新绑定 → 恢复验证。

首版只支持冷备份，恢复持久状态，不迁移进程内存或正在播放的会话。导出期间禁止相关环境修改并锁定资源引用，防止材料被清理；宿主共享目录也须停止写入，否则排除并说明，不以暂停容器代替完整一致性。

迁移管理记录采用语义化快照，不复制活跃 SQLite 与 WAL，也不覆盖目标机整个数据库。账号设备绑定、密钥环和服务端登录不保证迁移成功；必要时重新登录。

### 8.2 后端快照

Linux：对停止写入后的容器变化形成受控镜像快照，再通过 save/load 处理镜像；独立 HOME、volume、bind mount 单独备份。Docker commit/export 不自动包含挂载数据。[Docker commit](https://docs.docker.com/reference/cli/docker/container/commit/)、[Docker save](https://docs.docker.com/reference/cli/docker/image/save/)、[Docker export](https://docs.docker.com/reference/cli/docker/container/export/)

导出规范化环境描述，不复制整个 Docker data-root，不原样重放旧 inspect。恢复时重建环境 ID、目标用户映射、挂载、显示、音频、D-Bus 和设备配置。Distrobox 的用户名、初始化标记及驱动集成按所选版本验证，不全文替换路径或递归改变整个系统所有权。[Distrobox 保存恢复](https://distrobox.it/useful_tips/)

Windows：优先使用上游支持的完整 bottle 导出/导入；仅配置导出不够。runner、DXVK/VKD3D、Flatpak runtime 和外部数据需单独核查，不假设全部包含在 bottle 备份中。[Bottles 完整备份定义](https://github.com/bottlesdevs/Bottles/blob/main/po/bottles.pot)

上游 GUI 支持不等于自动化接口稳定。核实实际调用方式；只能人工操作时进入 waiting_user 并验证产物，不改写私有数据库伪装自动完成。无法可靠恢复的组合不标为可迁移。

### 8.3 归档格式

首版普通归档使用带版本的 .zahbundle（tar+gzip），Go 库流式处理。布局：

    manifest.json       格式/架构/需求、包含与遗漏内容
    catalog.json        应用、版本、实例、配方和追溯记录
    objects/sha256/     去重安装材料与运行时材料
    environments/      Linux 镜像 / Windows 备份
    data/              受管数据及用户选定外部内容
    bindings.json      目标机待重新绑定的目录和能力
    checksums.json     内容摘要、长度

默认包含受管环境、私有数据、安装材料和管理配置。外部音乐库等逐项选择“打包”或“在目标重新绑定”；不得自动打包整个 HOME，也不能悄悄遗漏内容。资源状态标记为已打包、目标需准备、待绑定或未包含。

只有材料与目标已有依赖共同满足条件时才标离线就绪。共享对象去重，Docker 层优先利用上游归档能力；不实现块级增量备份。检查目标/暂存空间和单文件限制，写 .partial，校验完整后在同一文件系统原子提交正式归档。

提供成熟加密格式的可选封装，密码/收件人输入不得进入日志或命令参数。具体格式与库在实现验证中锁定，完成前不能发布“加密迁移”能力。明文导出明确提示可能包含个人数据；摘要不提供保密性，也不证明来源可信。

### 8.4 导入与冲突处理

先解析清单，检查格式版本、架构、大小和摘要，再计划恢复。普通数据解包拒绝路径穿越、越界链接、设备节点和未经批准的特权元数据；合法外部链接记录为待绑定对象。Linux 镜像归档由后端按受控计划载入隔离环境，不在宿主直接展开成系统文件。

默认导入为新实例并建立旧 ID→新 ID 映射，重写其引用；同摘要材料可复用，同标签不同材料不可合并。新内容暂存，完成验证后提交目录登记；失败只清理本次资源，目标已有状态不变。

目标检查分为可恢复、需准备/绑定、当前不支持。缺后端按支持流程准备；不原样执行归档脚本、特权命令或挂载。恢复目录权限及宿主接口后重新验证；旧验证记录仅作为来源保留。

首次支持同 CPU 架构、实测宿主组合。必须在另一台不同用户名/HOME 的电脑验证，而不只测试原机原目录。未打包数据、缺 runner、重新登录等情况必须在向导中明确说明。

## 9. 离线、分发与权限

JS、CSS、组件、图标、文本、帮助和配方全部随包提供。禁止运行时 CDN、远程字体/主题/脚本；动态图标先净化或转码。新配置、空缓存、断外网条件下首次打开也要完整显示。

前端资源放 app.asar，Go 二进制放归档外固定目录。ASAR 不是加密，不把二进制放入归档后依赖运行时提取。[ASAR](https://www.electronjs.org/docs/latest/tutorial/asar-archives)

主包包含 Electron、Go、前端和必要私有库；外部运行后端按需准备并列出下载成本。无后端时管理界面仍可用。已有实例启动不等待更新检查；网络下载有超时、有限重试、取消和摘要校验。

静态协议与 preload 限定允许操作。renderer 开启 sandbox/contextIsolation，关闭 nodeIntegration，校验 IPC 来源和参数，禁止任意导航与新窗口；CSP 约束资源。外链只在用户明确操作且校验协议后打开。[Electron 安全](https://www.electronjs.org/docs/latest/tutorial/security)

用户空间库放私有目录，不替换宿主库；清理子进程环境，不把 Electron 库路径传给 Docker/Wine。图形应用以普通用户运行。root helper 必须独立、管理员拥有且校验操作、资源、参数和路径，不接受任意 Docker argv 或用户脚本；未验证的授权流程使用明确的人工准备步骤。

不默认 --no-sandbox、不长期固定停止维护的 Electron、不通过关闭 HTTPS 验证修复网络问题。敏感令牌优先宿主密钥环，不可用时不静默明文持久化。不宣称防范同用户恶意进程。

## 10. 性能与发布要求

以下为待测工程预算，采用四核、8 GB 内存、SSD 基准机：

- 完整压缩主包 ≤200 MiB，展开安装 ≤500 MiB；后端和应用另计。
- Electron 全进程组与 Go 的空闲 PSS 总计 ≤350 MiB；另报 RSS 并注明口径。
- 冷启动完整可交互 ≤3 秒，Go ready ≤500 ms。
- 本地缓存请求 p95 <100 ms，操作受理反馈 <200 ms。
- 前端总产物 ≤5 MiB；首屏 JS+CSS gzip 体积 ≤500 KiB，仅作体积度量。

首页不扫描所有安装包、不计算全库摘要、不启动后端或等待网络。CLI 启动不打开 Electron。测量真实打包产物和完整首页，至少重复 10 次，分开记录冷/热缓存、初次配置、断网、服务就绪与可交互时间；空白窗口不算完成。

正式发布提供 LICENSE、THIRD_PARTY_NOTICES、SBOM、签名清单、变更记录、性能报告、安全报告入口和支持矩阵。许可证由所有者决定，依赖/素材及私有库逐项落实再分发义务；不默认捆绑用户应用。

版本、架构、摘要和大小进入签名清单，私钥不进源码/安装包/普通日志，公钥通过可信发布途径提供。首版主动更新，不设置商业激活或强制联网；未来自动更新采用成熟安全框架。[TUF](https://theupdateframework.io/docs/overview/)

Electron、Go、协议作为整体升级。停止接受新修改任务，等 worker 安全结束，备份管理数据库，验证新目录并迁移后切换入口；失败保留旧版和数据。卸载管理器不默认删除应用环境。正常版本目录分发不使用启动自解压或在线引导补齐主程序。

## 11. 实施顺序与验收门槛

### 11.1 先验证部署与上游契约

锁定 Electron、Go、SQLite 驱动及前端版本。目标旧桌面不预装浏览器/开发运行时，验证完整主包的依赖、沙箱、中文输入、文件选择、绘制、断网启动和性能。Electron 精确支持范围以实测与上游维护状态共同判断。[Electron 平台政策](https://github.com/electron/electron#platform-support)

验证 Distrobox 首次创建、普通用户启动与授权取消；验证 Bottles 安装、入口发现、单应用退出、完整备份/恢复的实际接口。记录自动/需交互/不支持，并以此控制界面能力。

此阶段同时冻结 RPC 上限与版本规则、迁移加密格式、支持的镜像/runner 和权限安装流程；结果写入 docs/compatibility。未解决的部署或恢复阻碍不得以默认兼容替代。

### 11.2 完成应用生命周期

先实现材料归档、实体、引用、计划、锁、SQLite 与 IPC，再贯通 Linux 安装/启动/文件/清理，随后接入 Windows。安装包删除后能重装；同版本同材料去重，不同材料不覆盖；两个版本启动正确且配置独立。

QQ 音乐作为候选验收应用，记录实际包摘要；验证登录、本地与有权播放的在线音乐、宿主音量/输出切换、目录下载、重启及连续播放 30 分钟。未提供或未测试的安装包不标兼容。附加媒体功能逐项记录。

### 11.3 验证故障与数据边界

测试重复提交、双 CLI、manager/worker 崩溃、外部修改环境、权限取消、磁盘不足、包数据库忙和日志过量。恢复不得重复安装、误杀其他实例或删除已有资源。

测试中文/空格路径、只读别名、外置盘失效、真实文件操作与卸载保留内容。关闭界面后任务继续、应用继续运行；重新打开正确续接。能力记录关联实际宿主/运行时/应用版本，版本变化后重新验证。

### 11.4 验证跨机器恢复

测试 Linux/Windows 两条路线的受管状态、安装材料、外部内容选择与离线依赖完整性。另一台不同用户和路径的兼容机器导入后，验证入口、文件、配置、声音和必要登录，不只检查卡片出现。

测试名称/ID 冲突、缺资源、架构不符、坏摘要、恶意链接、超大归档、错误口令、导出取消和导入中断。测试环境可移除源副本以证明恢复独立；不得以用户工作环境做破坏性验证。

### 11.5 发布完成条件

通过离线安装、完整启动、性能、两后端功能、多版本清理、迁移恢复、签名及许可检查后，才发布对应范围的稳定版。框架支持、材料齐全、安装成功和实际功能通过分别记录。

历史 Python 原型的来源解析、进程身份、容器和认证经验先转为行为测试，再由 Go 实现；原部署不自动替换，明确导入后才接管资源。本文规定目标规格，精确版本和验收结果随实现补充，不修改既有应用来假装完成新架构。

### 2026-11-01 产品交互收敛

应用中心首页按应用显示版本下拉与运行按钮，内部实例/配方/环境细节放在高级管理。各版本保留独立可写环境，不为避免多开而共用安装目录；应用级互斥为强制规则。唯一批量停止按钮只影响登记且身份匹配的 ZaH 环境，确认包含未保存数据风险，不能使用全局 Docker/Wine 停止。ZaH 自身保留标准 Linux 最小化和最大化。

目标行为是外部受管应用点 X 即退出并停止其专属环境，短暂提示且不重复确认；最小化保持运行，可选的外部窗口最小化/全屏不阻碍核心交付。**当前实现仍未接通外部 X 意图。** 已实现的 Linux process-tree 监督仅在配方明确适配时于真实进程树退出后回收环境，不能把托盘化、隐藏或启动器返回冒充点 X 已关闭。具体源码完成范围以 DEVELOPMENT_VALIDATION 为准。
