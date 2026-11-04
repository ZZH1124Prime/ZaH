# 开发态验收与实际支持范围

日期：2026-11-01；对象为当前未提交工作树。Go、Electron、React 已有可构建、相互联通的开发实现。本记录区分代码实现、隔离验证和实际应用验收；不代表全部技术规格已达到稳定发布条件。

本轮未执行 Git stage/commit/push/reset，保留用户原有修改和原型迁移删除记录；未制作安装包、安装宿主桌面入口、签名或发布。现用 SakuraCat、代理、网络、DNS、路由、防火墙、Docker服务与生产容器未作测试或修改。

## 最终检查

| 检查 | 结果与口径 |
| --- | --- |
| Go 1.27.3 `go test ./...` | 全仓通过；含真实隔离 manager/worker 生命周期测试 |
| `go test -race ./...` | 全仓通过；生产安全门禁和测试夹具最终对齐后再执行 |
| `go vet ./...` / `go mod verify` | 通过，所有 Go 模块校验通过 |
| `npm run build` | Go、Electron TypeScript、React TypeScript/Vite 开发产物构建通过；不包含 Forge 分发打包 |
| `npm run test:desktop` | 31 项通过：协议、来源、参数、文件授权、Go/TS白名单及游标输入 |
| `npm run test:frontend` | 68 项通过：管理流程、计划/幂等、清理、加密、共享目录、事件恢复 |
| `npm run test:manager` | 真实 Node→Go ready-fd/握手→隔离 SQLite；客户端断开后观测 manager 自行空闲退出 |
| `npm run test:electron` | 真实 Electron→preload→React→Go→SQLite 无头联调，包括初始快照、增量事件和失效游标恢复 |

构建 Go 时设定 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1`；`file bin/zah` 结果为 x86-64 静态 ELF。Node/Electron 开发依赖固定于 package-lock；SQLite、age 和 x/sys 固定于 go.mod/go.sum。

Electron测试使用独立 HOME/XDG，删除测试子进程的 DISPLAY/WAYLAND_DISPLAY，保持 sandbox/contextIsolation，不使用 `--no-sandbox`。renderer 没有 require/process，bridge仅 call/chooseFile/chooseDirectory/saveBundle。未授权文件与任意shell方法被拒绝；实际登记的应用通过事件刷新呈现在真实React页面。截图为 `test-results/desktop-smoke.png` 和 `test-results/desktop-migration-smoke.png`。测试退出后等待隔离manager空闲退出再清理数据，不按进程名称杀用户程序。

Go集成实际验证了：manager被终止时worker仍完成材料归档；新manager读取同一任务和幂等结果；没有重放安装。真实Linux应用生命周期另见 [REAL_ENV_RESULTS](REAL_ENV_RESULTS.md)，不由worker测试推导。

## 规格能力与代码证据

| 能力 | 实际实现与验证 |
| --- | --- |
| 数据与入口 | 一个Go程序的CLI/manager/worker；SQLite WAL、短事务、稳定ID、引用、不可变计划及幂等任务；单测与真实本地IPC通过 |
| 材料 | 独立复制、SHA256复核、无覆盖提交、来源追溯；源删除、重复、损坏、中文、限额和取消测试 |
| 安装前门禁 | 在任何安装写入前检查材料/HOME/本机经典Docker数据根容量及linux/amd64固定镜像；不足或未知进入人工等待并保留unprepared。安装前再查普通UID、实际镜像、HOME与绑定 |
| Linux生命周期 | 精确整环境停止；显式 process-tree 配方的静态子reaper监督、nonce启动/完成证明、退出后停环境；模拟链与真实隔离double-fork测试通过；真实deb/X11运行与显式停止闭环见实环境结果 |
| Windows 安装与人工完成 | EXE/ZIP 引导准备专属 Bottle；用户操作原生安装器、明确选择最终入口并保存；上游人工导入后仍需验证程序、runner、PE入口；D:–Y:人工Winecfg映射与Flatpak有效路径权限取证，每次启动重核；全部mock与临时文件验证 |
| 应用互斥 | 同AppFamily所有版本强制互斥，不依赖可选配方组；不同应用可并行，额外conflict_groups仍有效；未知/中断占用不自动清除 |
| 简化界面与关闭 | 每应用版本下拉+直接运行，高级管理保留技术细节；唯一全部停止确认；ZaH原生关闭确认后核验成功任务和逐项证据才退出，失败留窗，普通最小化/最大化保持 |
| 批量停止 | 冻结受管身份和queued启动任务；执行前比较最新集合，取消冻结旧队列；监督器实际启动握手后才响应停止，不在延迟进入容器前盲停 |
| 清理 | 分离材料、精确受管容器、实例登记墓碑、私有HOME、自产快照镜像；各自引用/身份/活动进程/冷确认检查，不全局prune，不跟外部链接删除 |
| 备份 | 目录材料包、单实例完整包、多实例/全工作台冷备份；材料去重，未引用材料可包含；按绑定ID显式选择外部内容 |
| 数据恢复 | 新ID语义导入；HOME/所选外部副本在新受管目录准备后一次DB登记，错误回滚；重绑/运行时恢复/入口核验分阶段，解包不等于可运行 |
| 归档安全 | 限额、架构、内容摘要、穿越/重复/链接/设备/截断检查；源包审阅后替换会失败；共享目录采用os.OpenRoot锚定读取+独立暂存 |
| 加密 | age v1.3.2 X25519；实际Go库互操作、错误密钥/篡改/限额/取消/原子发布测试；私钥仅执行内存读取，计划只含选定文件身份元数据 |
| 事件与恢复 | 同一SQLite读事务中的snapshot/cursor；连续分页、不跳事件、历史缺口回快照；轻量通知不复制多MiB结果，断线只恢复读取 |
| 计划返回 | 未提交计划可弃置成cancelled审计，释放引用保护；已尝试但响应不确定的提交保留原plan/key，避免双任务 |
| 日志 | 独立worker有界日志，默认每任务64KiB、总16MiB/256文件；仅回收确认已停止的日志，活跃/未知/链接不碰 |

自动测试使用隔离目录、生成的测试密钥与模拟外部命令；没有读取用户真实私钥、真实应用数据或借用现用容器。数据库与文件系统不是跨资源ACID事务：普通错误回滚本次新资源，硬崩溃仍可能留下孤立材料/隔离目录，不能据此自动重放外部操作。

## 三类剩余边界

### 代码没有提供的自动化

- 用户已选择卡片运行/停止主路径，外部应用X强制退出及停环境不再是必需功能；外部最小化/全屏为未实现的可选项。窗口隐藏与启动器返回均不等于环境已停。
Windows 已有固定 runner/WINEPREFIX 的精确停止与受控启动握手；阻塞启动器、延迟启动和停止失败重试已通过隔离模拟，真实 Flatpak/Wine 仍未验收。支持条件见 [APP_RUN_STOP_ICONS](APP_RUN_STOP_ICONS.md)。
- 硬崩溃后未知应用/启动器的自动调和仍缺失。没有专用中断启动租约reconciliation API，`launching/needs_inspection`保持阻止重放；有这些未知目标或不支持停止的Windows目标时，全部停止失败，ZaH关闭流程会留窗。普通任务取消不是应用停止。
- 自动改写跨用户名Distrobox用户/初始化状态、自动操作Bottles/安装器 GUI 页面及完整导入、自动配置Winecfg/Flatpak权限。当前支持明确人工步骤后只读取证；这些不是伪造的自动成功。
- 自动安装/授权helper、通用任意runner或共享运行时垃圾回收。回收范围仅可证明由本机导出任务创建的精确无引用快照镜像。

Linux专属环境停止以完整ID/名称/标签核验；默认配方不从外部窗口事件推断退出。仅显式 `lifecycle: "process-tree"` 配方允许在完整子孙树证明结束后自动停止；既存D-Bus/systemd服务代启动不属于该证明。

### 已有代码但未完成真实验收

- Linux自制deb首次创建/安装、普通UID、真实X11窗口、保存与停止已经实测；第三方安装器、资源峰值、AppImage及Windows实际Bottles/runner/Winecfg/Flatpak行为仍待验收。
- 可见桌面窗口、原生文件对话框、中文输入法；无头截图不能替代这些检查。
- 指定应用的读取/保存/下载/重命名/删除、登录、音频、媒体键及持续播放；没有QQ音乐或SakuraCat新版兼容声明。
- 另一台不同用户名/HOME机器的完整恢复、离线依赖准备和应用登录状态。

这些必须先指定独立测试应用、材料摘要、测试容器/瓶和目标机器，经用户许可后执行；不能使用现用SakuraCat或生产容器。Docker镜像回收另要求所有外部Docker修改暂停；CLI无原子“仍无标签才删除”条件，ZaH锁不约束外部客户端。

### 明确不支持的组合或数据语义

- 非linux/amd64、OCI镜像布局、无法验证的归档版本/格式；当前Linux镜像恢复接受classic无标签单镜像save。
- 远程/VM/转发Docker容量推断、非classic overlay2/vfs或containerd snapshotter存储适配；原生本机daemon是执行前提，Unix地址本身不能证明它未被转发。
- 任意 Bottles runner/WineBridge/自定义包装配置的通用停止；仅支持上述精确控制条件。已有旧sandbox是否更新权限不能由`flatpak info --file-access`推断。
- ACL/xattr、原UID/GID、硬链接拓扑、特殊文件、越界/悬空/循环链接的完整重建；外部共享树绝对符号链接明确拒绝。
- 自动迁移密钥环/设备绑定/运行内存/服务端登录授权，不声明OfflineReady。age只提供保密和密文完整性，不提供发送者身份认证，不删除原明文或承诺安全擦除。

取消登记保留的HOME和外部恢复副本不再属于活跃工作台备份范围；应在取消前完成需要的备份，历史位置仍可通过审计Get查到。私有数据删除仅删除该实例HOME，不删相邻bindings副本或环境归档。

## 后续由用户共同完成

最终Forge安装包、宿主安装/卸载、签名/许可/SBOM、正式支持矩阵和完整产品性能预算按用户要求留待一起完成。前端构建产物体积不是完整安装体积；没有完成10次冷启动、PSS、断网全新安装等发布验收。

开发启动：项目根运行 `GO_BINARY=/home/zzh/.local/opt/go1.27.3/go/bin/go npm run dev`。它会打开真实窗口并创建用户级ZaH管理数据，由用户主动运行；没有后端也可查看管理界面。测试使用自己的临时XDG，勿指向真实应用目录。

模块概要见 [RELEASE_READINESS](RELEASE_READINESS.md)，核心数据流见 [GO_CORE](GO_CORE.md)；通信见 [GO_RPC](GO_RPC.md)，桌面见 [GO_DESKTOP](GO_DESKTOP.md)，归档见 [GO_MIGRATION](GO_MIGRATION.md)，日志见 [GO_LOGGING](GO_LOGGING.md)，运行时和加密证据见 [BACKENDS](compatibility/BACKENDS.md)、[ENCRYPTION](compatibility/ENCRYPTION.md)。

## 本轮真实验收与修复

用户已批准Z1上最多三个专用Linux测试容器及容器内官方依赖。实际结果、失败原始证据、修复、包清单、截图和清理回执见 [REAL_ENV_RESULTS](REAL_ENV_RESULTS.md)。首次进入容器初始化被30秒探测中断、Docker26停止参数不兼容均在真实测试中发现并修复；补充显式安装核实和停止失败重试，不篡改数据库状态冒充通过。

最终前端68/68、Electron31/31，全量Go test/race/vet/mod verify、开发构建、Node/Go和headless Electron均通过。真实Electron界面已对已安装的1.0/2.0版本执行切换、运行、单停、全停确认/取消；不是静态示例。原生关闭对话框仍未自动操作；生产CloseCoordinator使用真实后端加注入confirm/quit回调验收取消、失败留窗与重试后允许退出。

停止计划仅保证冻结并重新核验的目标；其他CLI在停止完成后发起的新启动仍是新操作，不是跨所有客户端原子shutdown。界面关闭期间阻止本窗口新写请求。材料/图标引用更换、tombstone恢复、未知提交重试、持久批次锁及遗留计划弃置均有回归覆盖。源码当前116个Go文件（含1个生成文件、53个测试文件）；计数不是本轮新增量。

旧版重复阶段记录在仓库外清理备份的 `documentation-before-results/docs/DEVELOPMENT_VALIDATION.md`，当前页只保留现行结论。无Git写操作、宿主依赖安装、网络代理变更或分发打包。

## 默认目录启动故障回归（2026-11-01）

用户的 `npm run dev` 已完成全部构建，但 manager 在就绪消息前退出。只读核验及受限复现取得退出码 1 和原始错误 `writable ancestor directory rejected: "/home/zzh/.local/state"`：该共享父目录为 0775，默认 `state/zah` 尚不存在。此前测试全部显式使用 0700 XDG 根，遗漏了真实默认目录布局。

修复不修改共享目录权限、不删除/重建数据库、不搬迁已有状态。仅在未显式设置 `XDG_STATE_HOME`、默认 ZaH 状态目录尚不存在且父链因可写权限被拒绝时，在已校验的 ZaH 数据目录内新建 `state/zah`。本机解析结果为 `/home/zzh/.local/share/zah/state/zah`。已有状态、显式路径、不可信所有者和符号链接仍严格检查；若默认与替代状态目录同时存在，拒绝自动选择并要求先核查。替代目录一旦建立，会保持使用，避免共享父目录权限改变后悄悄切换数据位置。

Electron 启动错误现在带退出码和有界 stderr。并发连接合并，失败自动调用采用 5/10/最多30秒退避且最多3次，之后需要用户明确重试；事件读取失败停止轮询，不重放写操作。预期业务失败通过受限 IPC 错误信封返回，preload 转为普通 Promise rejection，避免 Electron 连续打印 handler 异常。界面保留“重试连接”。

新增默认 HOME + 0775 state 父目录的真实 Go manager 启动回归，30个并发调用/失败缓存/显式重试及原始错误测试，以及界面停止轮询后重试恢复测试。完整 Go test、race、vet、开发构建、桌面33/33、前端69/69、Node→Go联调通过。无头 Electron 也改用默认 XDG + 0775 父目录，验证 React→preload→manager→SQLite闭环及父目录不变；测试输出与数据独立，原项目 bin/desktop dist/frontend dist 的既有文件摘要保持不变。

本轮没有启动用户默认数据目录中的 manager、没有改动默认目录权限/数据库、没有操作容器或宿主服务。Mesa 驱动权限消息与本次 Go manager 退出原因独立；未改变驱动、沙箱或系统安全设置。无头测试不验证用户桌面的GPU渲染路径，真实可见窗口需要用户重新构建启动后观察。


## 2026-11-01 三页重构独立验收

本次将普通 UI 收敛为应用库／管理／迁移，移除高级管理、材料及任务页面；保留 Go 的持久执行、安全校验和操作恢复。测试随用户流程变化替换原高级页面断言，旧页面源码和测试先备份于 `/tmp/zah-three-page-before`，未删除后台功能测试。

独立构建目录 `/tmp/zah-startup-validation-_407gnfr`：Go、Electron、React 构建通过；桌面 35 项、当前三页前端 32 项通过；真实主进程客户端→Go→SQLite 与关闭协调器回归通过；无头 React/preload/Go 回归通过。无头测试只允许模拟 Docker 镜像清单，其他 Docker 调用拒绝，验证继续安装时缺少前提的可见失败，没有启动安装器或真实容器。

Go 全量测试、race、vet 通过，日志 `/tmp/zah-three-page-go-{test,race,vet}.log`；新增应用平台／重复版本、只读依赖命令、同一计划不重复执行回归。测试兼容旧数据库中同计划多任务历史，保留其引用保护。未修改用户真实数据库；没有 schema 迁移。

当前截图为 `test-results/desktop-smoke.png`、`desktop-management-smoke.png`、`desktop-migration-smoke.png`。应用库截图包含实际私有 SQLite 版本及不同图标记录，未安装实例的运行按钮正确禁用。已检查实际像素；可见 GPU/原生选择器交互由用户继续验收。

此前自动合并迁移写入被自动审批拒绝而没有执行；用户随后明确授权稳定 ID 规则，本轮已完成合并和指定目录恢复（见下节）。当时自动重建可运行环境仍是实现缺口；后续实现与验收见本文件末尾。随后补充 Windows 精确删除及自动冷导出：模拟 runner 的准备→停止证明→删除闭环、相邻 Bottle／外部符号链接目标保留、身份替换拒绝，以及完整备份校验／拒绝覆盖／拒绝外部链接测试通过；未执行真实 Wine。真实 Bottles 未安装、未运行；这批测试不表示已达到全部产品需求或稳定安装版就绪。


## 稳定身份合并与依赖安装回归（2026-11-01，后续轮次）

`migration_merge_test.go` 验证重复版本不创建目录、不改变既有 family 更新时间／默认实例、不覆盖用户修改过的文件；新版本加入同一稳定身份。同名不同身份、平台／架构／版本差异分别覆盖。`restore_root_test.go` 验证所选目录中新建私有子目录、父目录无关文件保留、设备／inode 被替换后拒绝运行／删除、父目录证据变化和符号链接拒绝。归档内容不能指定目标路径，Electron 只接受原生目录选择授权。全部使用临时服务与合成归档。

`dependencies` 测试只使用注入 runner，覆盖只读模拟、未确认拒绝、已确认固定包、过期计划、取消、未知发行版、删除／升级拒绝、授权失败；没有真实执行 apt-get 安装、pkexec 或添加软件源。Electron `dependencies.test.ts` 覆盖原生取消零提交、重复点击合并、提交响应丢失后同计划重试、非依赖计划拒绝；security 测试禁止 renderer 自称已确认。Go 保存确认字段并要求 worker 持久任务，进度日志限制行数／行长，隐藏 URL 和敏感字段。原生 polkit 窗口及真实安装未验收。

审查发现并修复两项真实回归：三页 UI 原本丢失失败 Linux 安装的入口检查动作，现恢复“检查安装结果”，有活动租约时不提供该动作；任务提交成功后读取状态断线原本可能创建新安装计划，现保留持久 job ID，重试继续读取原任务。宿主检查不再先无限收集进程输出。

### 旧 76 项到三页测试的覆盖核对

此前 76 项包含旧 App.test.tsx 的 60 项和其他测试；三页初稿降为 32 项，不代表逐条等价替代。本轮按下面类别补回安全交互与状态断言，不能只用总数证明覆盖。

| 旧断言组 | 当前归宿／处理 |
| --- | --- |
| 空载不执行、应用创建、版本选择、直接运行、键盘激活、重复点击 | App.test.tsx：三页只读首次加载、平台创建、持久 queued 任务禁重复、Enter；后台 launch_groups/lifecycle 保留 |
| 全局停止确认取消、逐项失败、持久批次锁、取消当前批次 | App.test.tsx + task-state.test.ts + desktop/tests/close.test.ts；批次任务期间其他写操作保持禁用 |
| 活动版本锁、Windows 阻塞启动器可停、不同应用并行、未知租约不冒充停止 | App.test.tsx + lib/api.test.ts + service/windows_lifecycle、launch_stop_retry；保留精确环境身份边界 |
| 响应丢失幂等、返回后重试、孤立计划恢复／放弃、并发提交拒绝 | operations.test.ts + storage/plan_review_test.go、store_test.go；新补状态轮询断线不重放。旧计划评审弹窗已移除，不宣称其关闭行为仍存在 |
| 图标版本绑定、提取失败 fallback 不影响启动 | App.test.tsx + service/icons_test.go；安装包选择仍用原生文件授权 |
| 原生文件选择取消／重新打开、检查依赖相同结果／失败重试 | App.test.tsx、HostPanel.test.tsx；新增 DependencyInstall.test.tsx 原生确认取消、未知发行版、重复点击、未知结果重试 |
| 安装器退出不等于完成、EXE 模式／入口选择、保存失败保留信息 | InstallationGuide.test.tsx + service/installation*_test.go；Windows 原生 GUI 真实交互仍未验收 |
| 失败 Linux 安装只核实入口、不重跑安装器；活动租约禁核实 | App.test.tsx 恢复对应动作；service/verify_installation_test.go 保留后端强制检查 |
| 冷导出需停止／选定路径；共享内容默认排除、子集不能称完整目录 | 新 UI 只导出所选应用及专属数据，不提供共享目录选择；desktop/security 与 service/workbench/external_backup 测试保留；旧多次勾选冷确认页面已移除 |
| 区分材料／环境包、还原后不可直接运行、绑定不得继承旧路径 | App.test.tsx、migration_merge_test.go、environment_backup_test.go、external_backup_test.go；旧人工载入／验收 UI 已移除，当时自动可运行恢复未实现；后续新增恢复卡片与串联回归，见末节 |
| 删除确认、引用保护、部分失败不丢卡片、不擅删共享数据 | App.test.tsx + cleanup_instances_test.go；新 UI 将停止／删环境／删专属数据／忘记登记串联，原高级单独操作页不保留 |
| 原始配方编辑、目录绑定、能力记录、桌面入口预览、Wine drive 设置 | 普通页面已移除；相应 Go service/backend/host 校验测试保留。无对应普通 UI，不宣称同等 UI 覆盖 |
| age 公钥／私钥路径、密文与明文输出、镜像清理审核 | 新普通页面不提供这些高级按钮；Go encryption/runtime_cleanup 与 desktop PathGrants 测试保留；不是本轮新 UI 功能 |
| 启动错误退避、显式重试、事件读取恢复 | desktop/startup.test.ts、lib/events.test.ts、useCatalogEvents 及默认 XDG 真实 manager/headless 验证 |

APT 安装参数依据 [apt-get 官方发行版手册](https://manpages.ubuntu.com/manpages/noble/man8/apt-get.8.html)，权限弹窗及取消码依据 [polkit pkexec 文档](https://www.freedesktop.org/software/polkit/docs/latest/pkexec.1.html)。没有为了消除图形警告修改 sandbox、驱动或系统目录权限。


最终验证结果：Go 全量 test / race / vet 均通过；Electron 单测 40 项、React 单测 46 项通过；隔离目录完整 Go＋TypeScript＋Vite 构建通过，真实 manager 握手／SQLite／退出确认与无头 Electron→preload→React→Go 联调通过。日志分别为 `/tmp/zah-merge-dependencies-go-{test,race,vet}.log` 和 `/tmp/zah-startup-validation-_407gnfr/merge-dependencies-{build,ui-tests,manager,electron}.log`。当前管理页截图实际查看过像素，依赖按钮及应用卡片可见。脚本仅使用假 Docker 前置检查；未触发本轮真实容器、系统授权或应用安装。

本轮未覆盖用户活动产物 `bin/zah`、`desktop/dist`、`frontend/dist`；开发输出均在隔离镜像。测试子进程已结束，未留下本轮测试 manager。桌面契约升级为 4，旧 manager 仍由用户正常关闭并等待空闲退出，不强杀。Git 仅 status/diff 等只读操作，无 stage/commit/push/reset。

## 自动环境重建与 Ubuntu 20.04 兼容核对（2026-11-01，最新）

受支持的 `environment.rebuild` 已实现并接入普通迁移流程。完整包先登记/保留数据，再逐个重建新增环境；失败留“恢复环境”入口，不重跑安装器，也不让未验证环境直接运行。旧 `environment.load/verify` 仍保留为内部精细操作，不是普通用户的必经步骤。桌面契约升级为 5，旧 manager 不被强杀。

新增回归及其证据：

- `backend/restore_test.go`：Linux 首次入口失败保留完整受管身份、同 nonce 重试只创建一次、外部同名/不同 nonce 拒绝；Bottle 缺 runner/不同用户名拒绝、发布后丢失数据库回复可重试、新目标配置保留、最终入口/精确控制证明、无应用启动；解压流摘要不符清理本次新暂存且已有目录不覆盖。
- `service/environment_rebuild_test.go`：合成完整包导出→所选私有目录恢复→验证失败→重试后 ready/stopped，笔记文件保持；过期重建计划不触发后端；未完成身份登记的创建不能从 stop-all 中遗漏，也不调用未知目标。
- `dependencies/installer_test.go`：Ubuntu 20.04 Linux 现有 APT 计划可用；Flatpak 1.6.5 明确拒绝并解释兼容阻碍；无 pkexec、安装或软件源变更。
- `frontend/App.test.tsx`：导入后仅对新实例顺序重建、部分失败继续其他项并显示原因、卡片重试只恢复环境、不重复导入/安装/启动；取消目录选择无任务。

最终 Go 全量 `test`、`test -race`、`vet` 通过。Electron 40 项、React 48 项通过；隔离完整 Go/TS/Vite 开发构建、真实私有 manager→SQLite/关闭协调器/空闲退出和无头 Electron→preload→React→Go 联调通过。日志 `/tmp/zah-rebuild-go-{test,race,vet}.log`，隔离目录 `/tmp/zah-startup-validation-_407gnfr/rebuild-{build,ui-tests,manager,electron}.log`。当前迁移页截图 `test-results/desktop-migration-smoke.png` 已实际查看像素。检查时没有本轮隔离路径的残留进程。

这不是新恢复适配的真实 Docker/Bottle 验收：后端命令只由 fake Runner 响应；manager/headless 使用隔离 XDG 和 SQLite，假 Docker 仅用于前置条件失败。未执行用户数据迁移、真实容器/应用启动、系统包安装、软件源修改；未覆盖源仓库正在使用的 bin 或 dist，未进行 Git 写操作或分发打包。

真实缺口：Linux 跨机器/用户/UID/图形及文件兼容，Bottle 实际 CLI/runner/组件行为和两前缀相互独立停止仍待验收。Z1 的 Ubuntu 20.04.6 旧 Flatpak 候选及不再维护的 PPA 不能作为当前 Bottles 正常安装方案。不能把这些未实测能力写成通过；通用跨用户名重映射、无身份证据资源自动接管、Bottle 原始基线自动清理属于未实现能力，范围与后续验收方案见 RELEASE_READINESS。
