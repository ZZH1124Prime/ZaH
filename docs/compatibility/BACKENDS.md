# Go 后端契约与验证边界

本记录说明本轮代码实际完成的范围，不能作为 Linux/Windows 应用兼容认证。宿主既有代理、网络、服务和容器未作为测试对象。

## 证据与来源

只读检查本机 Distrobox `create --help`、`enter --help` 及安装脚本，版本为 **1.8.2.5**。初始只读核查后，用户已批准三个专用容器的Linux实测，安装/运行/停止结果见 [REAL_ENV_RESULTS](../REAL_ENV_RESULTS.md)。后续已构建并验证私有 Flatpak 1.18.4，见 [FOCAL_RUNTIME](FOCAL_RUNTIME.md)；尚未进行 Bottles/runner 安装和真实 Windows 验收。

核对的官方接口：

- [Distrobox create](https://distrobox.it/usage/distrobox-create/)：名称、镜像、自定义 HOME、附加包与卷。
- [Distrobox enter](https://distrobox.it/usage/distrobox-enter/)：明确命令、非 TTY、独立 HOME。
- [Bottles CLI](https://docs.usebottles.com/advanced/cli) 和 [官方 CLI 实现](https://github.com/bottlesdevs/Bottles/blob/main/bottles/frontend/cli/cli.py)：JSON bottle/program 枚举、bottles-path、按注册程序启动。
- [Bottles full backup 实现](https://github.com/bottlesdevs/Bottles/blob/main/bottles/backend/managers/backup.py)：完整 tar.gz 包含 bottle 目录，排除 dosdevices 和 lsfg-vk。ZaH 验证上游生成的产物，不调用私有 Python API。来源为核对时上游 main，不代表任意安装版本都兼容。
- [Docker commit](https://docs.docker.com/reference/cli/docker/container/commit/)、[save](https://docs.docker.com/reference/cli/docker/image/save/)、[load](https://docs.docker.com/reference/cli/docker/image/load/)：镜像层与挂载数据分离；load 还会恢复标签，不能把任意归档不经审查载入现有 Docker 存储。
- [Distrobox 保存恢复](https://distrobox.it/useful_tips/)：上游容器迁移方式仍需目标用户名、HOME 和集成验证。

## 已实现

`recipe.Decode` 读取最大 256 KiB 的单个 JSON 对象，拒绝未知字段、尾随对象、动态镜像标签、路径越界和 shell 配方。配方内容摘要与人工修订标签独立。Linux 镜像必须以 sha256 digest 固定；Windows 必须明确 runner。策略分别为 `appimage`、`deb`、`exe`、`msi`，不能互换。未实现的特权能力（包括网络接管）不能冒充普通 display/audio/files 能力。

`backend.Adapter.PlanInstall` 没有副作用。Linux 计划依次独立复制归档材料、创建独立 HOME 的容器、安装、核查入口。安装计划包含材料摘要及配方摘要；执行前重新核验。AppImage 在确认执行阶段才运行其自解包功能；deb 在受管容器中使用非交互 sudo/apt，无法授权则失败，没有宿主提权回退。

容器名称为 `zah-<instance-id>`，创建附加 `org.zah.instance` 标签。已有同名容器拒绝接管。后续操作要求完整 Docker ID、准确名称与标签同时匹配。镜像、材料、私有 HOME 和用户选定绑定由调用者持久化；后端不提供全局清理。

绑定保存真实目录 device/inode，并在后端执行前复核。Linux 目标限制在 `/mnt/zah/`；Windows 使用人工配置并核验的 D:..Y: 盘符。只读映射不意味着 Distrobox 内不存在其他宿主访问入口。Distrobox 1.8.2.5 的 `--volume` 实现按空白拆分；本实现生成固定的 `--additional-flags` 卷参数语法，支持中文和空格，拒绝会改变上游 eval 解析的 shell 元字符。并非所有合法 Linux 文件名都受支持。

入口检查和启动前还核查 Docker 实际 HOME 环境变量、HOME 与选定目录的 bind 源/目标/读写属性，并通过 Distrobox `id -u` 验证应用用户等于当前宿主普通用户。

`ExecuteStep` 的安装步骤成功退出仅记录 `installed_unverified`；只有显式入口检查通过才记录 `entry_verified`。普通启动接口仍在启动器退出后记录 `launcher_exited_application_unknown`；Linux 新监督启动接口另需等待全部受监督后代结束的 nonce 证明，不能从启动器退出推断应用停止。移除仅允许停止的、身份匹配的容器，不强制停止，不删除 HOME、共享数据或材料。

`process.OSRunner` 不使用 shell；清理 Electron/动态库注入环境而保留现有代理变量；输出按流限制并持续排空。可取消的检查有超时，副作用事务启动后等待自然结束，取消在下一安全边界处理。PID、启动 ticks、boot ID 共同组成身份；独立 Signal 使用 pidfd，旧内核不支持时失败，不降级为只按 PID 杀进程。

`host` 提供目录绑定身份复核、实际文件系统剩余字节、稳定实例桌面文件生成与不覆盖安装。`service.DesktopPlan` 先返回确切内容和目标路径，供服务将写入纳入用户操作。桌面快捷方式只调用 `zah launch <id>`。

## Windows 安装引导与可验证接续

新 EXE/ZIP 安装引导通过官方 CLI 创建唯一的专属 Bottle，把单 EXE 或整个 ZIP 普通文件树复制到该 Bottle 的 `drive_c/zah-material`，登记并核验入口。单 EXE 输入表示打开意图，准备后自动打开；ZIP 由用户选择要打开的 EXE，不区分安装器和便携模式。程序若显示安装选项，由用户操作；退出不代表安装完成。完成后显式停止环境、选定应用 EXE 并确认保存；既有人工准备/登记及完整备份恢复流程仍保留。`VerifyManualBottles` 使用官方 JSON CLI 核对准确 bottle 名、runner、win64、目录身份及唯一程序注册，拒绝 pre/post script；核验配方入口与实际 x86/AMD64 PE 文件，保存配置/程序/入口摘要。后续操作重新检查证据，改变时拒绝启动。`Launch` 调用官方按注册程序启动接口；启动器退出仍不代表应用停止。没有猜测 MSI 参数、直接编辑 Bottles 私有数据库或全局 kill。

安装材料只做静态识别：x86/AMD64 PE EXE、含对应 EXE 的 ZIP、amd64/all deb、x86_64 type-2 AppImage。ZIP 拒绝穿越、绝对/盘符路径、符号链接、特殊文件、大小写重复、Windows 保留名称、控制字符、文件/目录冲突、加密成员和限额超标；上限为 20,000 项、总展开 8 GiB、单文件 2 GiB。新目录原子发布且不覆盖已有目录；不会只复制 EXE 而丢弃 DLL/配置。单 EXE 按归档的原始文件名复制，不改名；依赖同目录额外文件的程序应提供完整 ZIP。旧版已登记的 `program.exe` 记录保持兼容。

引导仅列本地 Ubuntu 22.04/24.04 固定摘要镜像和本地普通 Wine runner，不自动拉镜像、安装 Flatpak/Bottles、改宿主权限。新 Bottle 的计划明确披露 `winebridge=false,sandbox=false`（该新 Bottle 的运行模式，不是关闭 Electron/Flatpak 沙箱），以满足现有精确 Wine 控制；Bottles 创建时自身可能下载官方组件。已有同名 Bottle 一律拒绝接管。部分创建失败保留 `needs_inspection` 和执行证据，不能盲目再创建。

“保存入口”与“安装维护”都是需确认的持久计划。入口须在已登记 Bottle 内，复核 PE、摘要、runner、prefix 身份并证明 Wine 已停止；保存配方与环境证据在一次 SQLite 事务中切换。安装器退出/自动启动子程序均不表示完成。安装器保留以供维护；维护不自动删除已安装数据。候选来自官方 programs 列表，找不到时可原生选择实际 EXE。高级人工映射 D:–Y:、MSI、任意自定义 runner/脚本及完整恢复仍不自动适配。

这条新引导与保存入口闭环通过模拟上游 CLI、临时 PE/ZIP 和数据库测试，**尚未运行真实 Bottles/Wine 或第三方安装器**。32 位 EXE、驱动、反作弊、微软商店/UWP、特殊系统服务不能据此声称兼容。deb 自动发现限简单 `.desktop` Exec 对应 `/usr` 入口；复杂启动参数或 `/opt` 布局需人工提供受支持入口/专用适配。官方接口依据：[Bottles CLI 文档](https://docs.usebottles.com/advanced/cli)、[上游 CLI 实现](https://github.com/bottlesdevs/Bottles/blob/main/bottles/frontend/cli/cli.py)。

`VerifyManualBottlesBackup` 对用户提供的完整 tar.gz 做归档安全、所有 bottle 普通文件及安装入口摘要核验，源树在验证前后必须一致。dosdevices 和 lsfg-vk 的上游排除项写入证据；外部 runner/DXVK/Flatpak 不假装包含。除这些排除项之外遇到链接或特殊文件明确拒绝，因此不是任意 bottle 都能迁移。目标侧 `InspectBottlesBackupArchive` 仅验证结构，用户用上游 GUI 恢复之后还需注册验证；恢复包不自动变为 ready。

## 冷快照和实例/工作台迁移

`ExportLinuxSnapshot` 要求准确所有权且容器停止，记录冷状态见证，依次 commit 无标签镜像、save、验证归档、复查冷状态后不覆盖发布。commit 生成的镜像 ID 即使后续失败也保留在结果中；不会全局 prune。仅镜像层不包含挂载 HOME，因此返回 `CompleteInstance=false`。

`InspectDockerArchive` 仅接受单镜像、无标签的 classic Docker save tar，验证 config 内容摘要、linux/amd64、每层 diff ID，拒绝标签、额外元数据、路径穿越、链接与特殊条目。OCI 布局明确不支持。`PlanLinuxRestore` 本地核验，`LoadLinuxSnapshot` 私有复制后复核并按内容 ID 复用或载入镜像，不从归档重放 inspect 配置或命令。目标容器重建仍为人工步骤；`VerifyManualLinuxRestore` 核对新受管名称/ID/标签、准确 image ID、实际 HOME/绑定、普通 UID 和入口后才可登记可用。

`service.ExportEnvironmentBundle` 组合镜像或完整 bottle 备份、管理语义数据、安装材料和受管私有 HOME。HOME 包含普通文件、空目录和指向内部的相对链接；外部/悬空/循环链接、设备、socket、FIFO 明确失败并报告路径。共享宿主目录默认只记录重绑要求；用户明确选择的 IncludeBindings 会独立复制内容，恢复到新受管目录供重新绑定，不覆盖原宿主路径。文件权限收紧到 owner 位，不保留原 UID/GID、ACL、扩展属性、硬链接拓扑或特殊文件语义；这些类型的应用不能据此推断可完整还原。

`ExportWorkbenchBundle` 支持多实例，材料按摘要去重；空选择表示所有当前实例并包含全库记录及未引用材料。`include_catalog=true` 的显式选择必须涵盖所有当前实例，防止漏掉实例而宣称全库。未准备/未注册环境会明确拒绝完整工作台导出；纯材料库可使用 catalog bundle。所有选定实例须持锁至发布，最终重查冷状态与私有树。Windows 实例必须逐一提供完整备份证据。

`RestoreEnvironmentBundle` 验证全部语义与载荷，重新分配所有实例/环境 ID，恢复到独立新目录，持久化待恢复镜像或 bottle 包；目录和文件同步后通过一次数据库事务登记。中途失败回滚新数据；提交结果不确定时保留新目录待核查。目标实例保持 `needs_runtime_restore`，环境保存 `PendingEnvironmentRestore`，绑定需要重选；不能把数据解包等同于跨机器应用已经可用。材料 catalog bundle 和完整环境 bundle 是不同格式。

## 明确边界

Windows 已有固定 runner/WINEPREFIX 的精确停止与受控启动握手；阻塞启动器、延迟启动和停止失败重试已通过隔离模拟，真实 Flatpak/Wine 仍未验收。支持条件见 [APP_RUN_STOP_ICONS](../APP_RUN_STOP_ICONS.md)。

跨用户恢复没有自动改写镜像内用户、绝对路径或应用数据库。人工重建后的 UID/HOME/入口检查不能证明应用所有业务行为、凭据、音频、显示或外部文件路径正确。所有归档 `OfflineReady=false`；操作系统、容器工具、Bottles/runner、登录授权和未打包的外部数据仍须在目标机器准备。真实 Docker 权限/首次创建、GUI、Bottles 精确安装版本、另一台不同用户及 HOME 的恢复均未执行；没有登记任何应用兼容认证，也未保证 QQ 音乐或 SakuraCat 运行。

## 验证证据

本轮 `go test ./internal/backend ./internal/host ./internal/recipe ./internal/process ./internal/service` 通过。覆盖人工 bottle 登记/篡改/脚本拒绝、冷镜像导出/载入/归档攻击、HOME/挂载/普通 UID 核查、完整私有树恢复与链接边界、多实例共享材料去重及未引用材料保留、第二实例数据库失败的全量回滚。此前 backend/host/recipe 与 process 的 race 检查分别通过。后端调用全部使用模拟 Runner；进程测试仅运行本次 Go 测试辅助进程。这些证据不替代真实应用兼容验收。

## 停止/不存在证据与受管快照镜像回收

Windows 已有固定 runner/WINEPREFIX 的精确停止与受控启动握手；阻塞启动器、延迟启动和停止失败重试已通过隔离模拟，真实 Flatpak/Wine 仍未验收。支持条件见 [APP_RUN_STOP_ICONS](../APP_RUN_STOP_ICONS.md)。

`VerifyAbsent` 成功查询所有容器中的受管名称，再查询登记过的完整 ID；只有两个查询均为空才能证明当前不存在。名称过滤故意保守，子串碰撞也拒绝回收。daemon 错误不当作缺失；调用者仍需持有全局/环境锁。

新的 Linux snapshot commit 通过官方 `--change LABEL` 写入 `org.zah.snapshot` 随机 256bit 标记与 `org.zah.snapshot.instance`；`SnapshotEvidence` 返回 SourceInstanceID、OwnershipToken、ImageID 和 ImageCreated。commit 后归档失败也保留这些证据；若进程未返回可核验 ID，不猜测或扫描删除。

`PlanSnapshotImageRemoval` 仅接受本机持久化 export 任务的创建证据，不接受裸 image ID、load 结果或导入包声明。计划和执行核对准确 image ID/标签、没有 RepoTags/RepoDigests、所有容器无 ancestor 引用。`RemoveSnapshotImage` 要求计划显式 `external_quiet_confirmed=true`，执行时复查两次，再使用 `docker image rm --no-prune <完整ID>`，不 force，不删除无标签父镜像，不 global prune。失败保持 unknown 并保留记录，不盲目重试。服务还必须核查库、待恢复环境和任务引用。

Docker CLI 没有“仅当仍无 tag 时删除”的原子条件；ZaH 锁不能约束外部 Docker 客户端，用户暂停所有外部修改是执行前提，复查不能消除这一限制。[Docker image rm](https://docs.docker.com/reference/cli/docker/image/rm/) 说明 no-prune 和非强制语义；[container ls](https://docs.docker.com/reference/cli/docker/container/ls/) 定义 all/ancestor 过滤。旧快照缺少标记时拒绝自动回收。

新增 stopped/absence、owned image 删除的全部测试使用 mock。包括缺字段/错误所有权、引用或 tag 变化、缺外部静默确认、非本机创建证据、daemon/删除错误及失败后创建证据保留。backend/service 测试通过，backend race 与 vet 通过；未执行真实镜像删除。

## 安装执行前提与当前 Windows 文件绑定边界

`PreflightInstall` 在服务把实例记为 installing 和写入任何安装数据之前执行。Linux 需要已在本机准备的准确 digest 镜像，image inspect 验证完整内容 ID、linux/amd64 和大小；不隐式拉取缺失镜像。只读 context inspect 检查 Unix endpoint，DockerRootDir 从 info 取值后必须能真实 stat/statfs；另核对 storage driver 只接受 classic overlay2/vfs，并拒绝 DriverStatus 中的 containerd/snapshotter，其他驱动或缺失证据需独立容量适配器。远程 daemon、未知路径或权限不足返回 manual，不能当作容量充足。此前未发生副作用的实例保持 unprepared；取消 waiting_user 任务后，准备环境并重新生成计划。

容量证据分别记录已存在材料库、受管 HOME 的最近真实祖先及 Docker 数据目录。HOME 估算为材料大小的 5 倍加 64 MiB；Docker 写入估算为本地镜像大小加材料的 4 倍加 256 MiB。相同 device 的需求合计后与实际可用空间比较。材料已经入库，因此此阶段材料库新增需求为零；此检查不代替材料首次导入容量检查。依赖下载量、其他进程占用、配额、薄池和应用额外写入不能据此保证，证据明确为估算。Unix socket 必须指向本机原生 daemon；被转发或虚拟机中的 daemon 不具备本机存储路径假设，不能使用该容量结论。

prepare_material 复查本地文件系统，create/install 再次执行完整预检。安装器运行前核对容器实际 image ID 与已检查的固定镜像一致，并检查真实 HOME/绑定与 Distrobox 命令选出的普通 UID；AppImage 自解包在这些门槛之后。deb 的非交互 sudo/apt 仅作用于已经核对身份的容器，没有宿主提权回退。官方接口：[Docker context inspect](https://docs.docker.com/reference/cli/docker/context/inspect/)、[Docker info](https://docs.docker.com/reference/cli/docker/system/info/)。这些执行路径有模拟回归，并已在本机Docker26.1.3/overlay2、固定Ubuntu22.04镜像上实测自制deb安装；不推广到其他存储驱动或第三方应用。

Windows 绑定使用 D:..Y: 的明确盘符，C:/Z: 保留；Linux 仍使用 `/mnt/zah/...`。人工在 Winecfg 配好后，登记入口时读取该 bottle 的 `dosdevices/d:` 等符号链接，解析结果必须等于所选宿主目录，并复核 device/inode。通过 Flatpak 当前访问等级核验后，将盘符、宿主目录身份、链接身份/目标和权限证据纳入 BottlesRegistration 摘要。每次启动都会重核；缺失、变化、超出请求权限或不能查询均拒绝，不能把 host.VerifyBinding 当成运行时配置已经生效。ZaH 不创建盘符链接或修改上游权限。

可只读核验的上游范围：官方 [Flatpak info](https://docs.flatpak.org/en/latest/flatpak-command-reference.html#flatpak-info) 的 `--file-access=PATH` 查询当前部署上下文的访问等级；[上游实现](https://github.com/flatpak/flatpak/blob/main/app/flatpak-builtins-info.c) 返回 hidden/read-only/read-write。它不启动应用、不更改权限，但也不能证明现存旧 sandbox 的权限已刷新或应用能够读写文件。Wine 的盘符映射依据 [Wine 手册的 dosdevices 定义](https://manpages.debian.org/unstable/wine-development/wine-development.1.en.html) 独立核对到所选目录，并核对所选 bottle 的身份；不从 Linux 目标路径推断盘符。

人工准备清单：在 Bottles 的 Winecfg/Manage drives 选择明确盘符和目录；按 [Bottles expose directories](https://docs.usebottles.com/flatpak/expose-directories) 仅开放所需目录；退出旧进程后核查当前权限；在目标应用实际验证读取/保存/重命名/删除与重启持久化，并分别记录结果。Flatpak 的权限范围是整个 Bottles app，不能声称隔离到单个 bottle；只读路径也不能证明不存在其他可写访问入口。上述人工配置不会被 ZaH 自动执行。当前支持的是人工配置后的映射与权限取证（configured_path_access），没有把旧运行中 sandbox、应用业务读写或替代路径隔离标为验证通过。新增测试覆盖正确 ro/rw、错误盘符/路径/权限、缺命令、父目录链接逃逸及启动前映射漂移，使用临时目录和 mock Flatpak 响应，无真实后端验收。


## 独立环境停止与监督生命周期

每个应用版本拥有专用环境。Linux `Stop` 是显式的整个受管环境停止：先以完整 Docker ID、准确名称和所有权标签核验，再执行 `docker container stop --timeout 10 <ID>`，最后严格 `ConfirmStopped`。按 [Docker stop 官方语义](https://docs.docker.com/reference/cli/docker/container/stop/)，先发送配置的停止信号（通常 TERM），10 秒内没有结束会由 Docker 强制结束该容器；不能宣传为一定能保存未保存数据的温和退出。请求失败或复核不完整保持 unknown，不扩大到其他容器、宿主进程、网络或代理。

`LaunchSupervised` 只接受服务准备并校验摘要的同一静态 ZaH 二进制，文件必须是受管 HOME/.zah-runtime 内私有、当前用户拥有、单硬链接的静态 amd64 ELF。固定调用 `zah supervise NONCE STARTED_PATH ENTRY`；nonce 为 32 位小写十六进制，started marker 由监督器在入口启动成功后持久化。适配器拒绝重复 marker，不把命令进程返回当成应用结束。

监督器使用 subreaper 等待入口派生的进程树（包括后台化的后代），应用输出独立送至 stderr；stdout 必须是协议 zah.supervise.v1 的单个完整 JSON。只有命令退出 0、stdout 完成证明未截断、后代状态不未知，并且 protocol/nonce/completed/entry_exit_code/reaped 全部通过严格校验，适配器才返回 `application_exited`，附带入口退出码与回收数量。缺字段、重复字段、额外对象或错误 nonce 均拒绝。仅 stderr 诊断输出超限不破坏独立 stdout 完成证明；stdout 超限或旧版只有笼统 Truncated 的不明确证据仍拒绝。该返回值不直接停止环境；服务在生命周期互斥门内安排停止，以协调显式停止请求。

Linux service 启动统一使用 started 握手来协调显式停止；仅 recipe 的 `lifecycle:"process-tree"` 启用退出后自动停止。默认配方不根据窗口消失或入口返回自动停止。

这证明的是受监督进程树退出，不是窗口关闭意图。关闭按钮可能隐藏到托盘；最小化不应被当作退出。经宿主 D-Bus、既存服务或不属于该进程树的进程代启动应用，不能由 subreaper 保证跟踪，需要单独适配；没有声称通用拦截 X11/Wayland 的关闭/最小化事件。

Windows 当前不调用上游 `bottles-cli stop -b`，尽管 [官方 CLI 源码](https://github.com/bottlesdevs/Bottles/blob/main/bottles/frontend/cli/cli.py) 存在这个命令。原因是 [wineboot.kill(True)](https://github.com/bottlesdevs/Bottles/blob/main/bottles/backend/wine/wineboot.py) 的强制后备路径会进入 [wineserver.force_kill](https://github.com/bottlesdevs/Bottles/blob/main/bottles/backend/wine/wineserver.py)，再用 [ProcUtils.get_by_env](https://github.com/bottlesdevs/Bottles/blob/main/bottles/backend/utils/proc.py) 对环境数据做字符串包含匹配并按裸 PID kill，不能保证前缀相似的其他 bottle 不被选中或避免 PID 复用。该源码路径还未向 CLI 传播 wait 的失败状态。没有用此结果冒充可靠 stopped 证据。

Windows 已有固定 runner/WINEPREFIX 的精确停止与受控启动握手；阻塞启动器、延迟启动和停止失败重试已通过隔离模拟，真实 Flatpak/Wine 仍未验收。支持条件见 [APP_RUN_STOP_ICONS](../APP_RUN_STOP_ICONS.md)。

本轮 backend 测试仅使用 mock Docker/Distrobox 响应及非执行 ELF fixture，覆盖精确停止范围、复核失败、外来身份拒绝，以及监督协议错误/不完整/截断/未知后代/重复 nonce 等拒绝路径。监督器自身真实隔离进程测试由 process 模块负责；没有执行真实容器停止、GUI、Bottles kill 或现用应用。

## 本机首次进入与停止参数实测修正

Distrobox1.8.2.5首次enter即便最终命令为id/test也会初始化依赖；受管enter在持久worker内等待，不用30秒可取消探测中断包管理事务。Docker26.1.3本机 `docker container stop --help` 支持 `-t/--time`，不支持 `--timeout`；现在使用共同短选项 `-t 10`，仍以完整ID及停止后状态复查为准。Linux停止失败保持监督控制并限频重试，每次复核身份，不释放占用或报告假成功。

### 三页管理中的 Windows 删除与导出（2026-11-01）

`bottles_remove.go` 在精确 Wine 前缀停止证明后，只接受默认 Bottles 根目录中、名称与登记 inode/device 一致的 ZaH 专属目录。拒绝自定义位置、身份替换、挂载点和特殊文件；先移入随机且不覆盖的隔离名称，再通过 `os.Root` 删除，不跟随符号链接。后台还拒绝未解除的启动租约／停止请求；移除后复查原目录及上游目录记录均不存在。失败可能保留隔离目录待检查，不盲目重放。没有调用上游私有删除函数、桌面文件通配清理或全局 Wine kill。[官方 CLI 文档](https://docs.usebottles.com/advanced/cli) 未提供普通 Bottle 删除子命令，因此这部分是 ZaH 的受限目录实现。

`bottles_export.go` 自动生成既有完整备份校验器接受的 tar+gzip 文件包，前后验证精确停止状态及完整文件集合／摘要，拒绝覆盖目的文件。仅保留既有校验器支持的普通文件；dosdevices 等已声明排除内容需目标重绑，其他符号链接或特殊文件明确报错，不复制其指向的宿主数据。runner、Flatpak runtime 等前缀外依赖不会因此自动打包。没有调用未核实的上游备份 CLI。

上述新增操作只有隔离目录及模拟 runner 回归，真实 Flatpak/Bottles 未安装或操作，不能推断全部真实前缀结构可用。应用自更新导致登记入口或配置摘要变化时仍会保守拒绝，需重新核实；当前简化 UI 尚缺一体化调和流程。Windows 目标端自动恢复仍未实现。
