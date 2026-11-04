# 应用卡片运行、停止与版本图标

本轮以用户确认的简单流程为准：选择版本 → 运行 → 同一按钮停止该应用环境。不同应用可以并行；同一应用的版本在运行期间锁定。外部窗口 X 不再是必须捕捉的事件，关掉窗口不意味着运行环境已关闭。

## 实际调用链

- `ApplicationLibrary.tsx` 根据持久实例/环境状态及尚未完成的任务显示运行、正在启动、停止环境、正在停止。选择框在活动期间固定到实际活动实例的 release，停止目标不会跟随其他版本选择漂移。
- `App.direct` 为单应用停止提交 `plan.create {operation:"instance.stop",input:{id:INSTANCE_ID}}`，然后 `job.submit`。不额外弹确认；卡片一直显示未保存数据风险。响应丢失保留原 plan 和 idempotency key，失败不冒充已停止。
- `Service.planStopOne` 从登记记录冻结实例、环境和完整后端身份；`executeStopOne` 只取消该实例尚未开始的启动任务，并复用 `stopTarget` 的控制门和身份复查。其他应用不进入该单应用目标集合。
- Linux 所有新的 service 启动现在都使用同一个静态 Go 程序的 supervise/started 握手。这让显式停止不会与尚未完成的 Distrobox 启动竞态。只有 recipe 显式 `lifecycle:"process-tree"` 才在进程树完成后自动停止；普通配方保留环境及运行租约，卡片继续提供停止。
- 全部停止和 ZaH 自身 X 确认流程保持独立；停止失败保留窗口和逐项错误。

## 版本图标的实际边界

`AppRelease.icon_sha256` 指向材料库中的规范化 PNG 对象。像素不放进 catalog.snapshot，避免每次事件携带多份图片。`release.icon.get {id}` 只读取该 release 已登记的摘要，返回经过校验的小型 PNG data URL。renderer 没有任意文件读取能力，也不加载远程图标 URL。

登记版本时，自动扫描不超过 32 MiB 的 ZIP、TAR、TAR.GZ：只接受唯一 `.desktop` 的 `Icon=` 与唯一 PNG basename 对应；成员总展开量最多 32 MiB、最多 512 项，拒绝越界路径、链接、特殊文件及歧义。扫描不解包到宿主、不执行安装材料，失败直接使用统一 Library 图标。当前**不支持 AppImage、deb、EXE/ICO/SVG 图标自动提取，也不自动扫描部署目录**。

点击卡片图标可选择本地 PNG，仅更新当前版本。`release.icon.set {id,path}` 必须获得 Electron 系统选择框的文件授权。Go 拒绝最终路径符号链接、非普通文件、超过 1 MiB 或尺寸超过 1024×1024 的输入；重新编码为最长边 64 像素、最多 20 KiB 的 PNG，去掉原始附加信息。原图删除不影响已保存图标。读取失败显示统一图标，不阻塞运行。

图标使用独立 `release → artifact` 的 `icon` 引用角色，受材料删除引用保护。目录、单实例及工作台备份收集该资源；导入重新映射摘要并重建引用。替换后的旧图标仍是可审查的未引用材料，不自动删除其他版本共用对象。

## Windows 精确停止适配与限制

新增 `internal/backend/wine_control.go` 使用 Wine 官方按精确 WINEPREFIX 定位会话的 `wineserver -k` 和 `-w`。不调用 Bottles `stop` 的 PID fallback，也不导入上游私有 Python API。固定脚本使用 Bottles Flatpak 运行时已有 Python 标准库，查验文件身份并通过 argv 调用 Wine；没有安装新 Python 工具。

人工登记兼容 Bottle 时，读取官方 `bottles-cli info bottles-path`，对照上游定义的相邻 `runners/<runner>/bin` 布局。记录 host/Flatpak 一致的 prefix device/inode、精确路径、实际 Wine/wineserver ELF 摘要、UID 和 Flatpak `/tmp` device/inode。停止前重新登记核验并比较所有证据；`-k` 后必须 `-w` 成功，超时、非零退出、截断、替换或命名空间变化都不能报告已停止。检查和等待有界，停止事务不接受中途随意杀控制进程。

目前支持范围是普通 Bottles 管理 runner、`Parameters.sandbox=false`、无自定义 Environment_Variables。系统 runner、Steam/Proton、外部 runner、嵌套沙箱以及没有完整控制证明的旧登记不自动停止；需要核实后重新登记，不降级全局 Wine kill。Flatpak 临时目录身份变化（例如重启）也要求重新登记。探测未通过的安装登记仍可保留，但不声称具有精确停止能力。

**尚未验证：**本机没有 Flatpak/Bottles，未对真实 Wine 执行任何 probe/kill/wait。这里只通过命令模拟及临时目录内惰性 ELF 夹具验证控制脚本。真实验证应使用两个隔离、无账户数据的 Bottle，停止其中一个并确认另一个不受影响。Windows 阻塞启动器的受控握手已实现并通过模拟，具体机制见下段；这仍不是实际 Wine 兼容认证。Bottles 不等同于 Docker 容器，Wine server 停止也不是对任意 native 辅助进程的回收证明。

Windows 启动还要求有效 `winebridge=false`，没有程序级环境覆盖、pre/post script 或 launch wrapper。宿主侧独立 `supervise-controlled` 进程拥有本次 Flatpak 启动树，入口成功创建后发布 nonce started，界面可显示停止（不等于应用窗口已正常显示）。Flatpak 内固定 Python 包装仅清理子进程的 Wine 控制变量、TMPDIR 与动态库注入变量，再 exec 官方 bottles-cli；与停止脚本使用相同命名空间规则，保留宿主代理/显示变量。

停止先发送 nonce 绑定的私有请求文件，由监督器用 pidfd 逐一回收自己的直接子进程及收养后代，等到 ECHILD 的完整证明后，才按精确 WINEPREFIX 运行 `wineserver -k/-w`。不遍历宿主全局进程、不按进程名或负 PID 杀组。已提交但延迟执行的启动也必须经过这道排空证明，防止“刚停完又启动”。请求文件原子发布、同 nonce 幂等；暂时写失败保留 watcher 限频重试，单次停止等待有上限。完成证明丢失时保留未知状态和互斥租约，不冒充已停止；若树已排空但 Wine 停止失败，可再次点击停止重试。

此路径要求 Linux pidfd/subreaper、普通受管 runner、显式关闭 WineBridge；不支持由既存外部服务代启动或自定义包装的任意程序。默认/未知 WineBridge 状态在启动前拒绝并给出重新登记提示。旧登记缺少控制证据时同样需要重新登记。真实 Flatpak 是否严格保留所需进程归属仍是双 Bottle 验收项。

上游核查是 2026-11-01 的 main 源码，不是本机安装版本认证：

- [Bottles stop_bottle / run_shell](https://github.com/bottlesdevs/Bottles/blob/main/bottles/frontend/cli/cli.py)：stop 调用 WineBoot.kill(True)；shell 经过 WineCommand，并非直接选择 wineserver 的接口。
- [WineServer](https://github.com/bottlesdevs/Bottles/blob/main/bottles/backend/wine/wineserver.py) 和 [ProcUtils](https://github.com/bottlesdevs/Bottles/blob/main/bottles/backend/utils/proc.py)：force_kill 使用环境串包含匹配和裸 PID；ZaH 不使用该路径。
- [Paths 布局](https://github.com/bottlesdevs/Bottles/blob/main/bottles/backend/globals.py)、[runner 选择](https://github.com/bottlesdevs/Bottles/blob/main/bottles/backend/utils/manager.py)：用于受限普通 runner 路径适配。
- [Wine wineserver 手册](https://dyn.manpages.debian.org/unstable/wine/wineserver-stable.1)：精确 WINEPREFIX 会话和前缀 device/inode 对应服务器目录。
- [Flatpak run 实现](https://github.com/flatpak/flatpak/blob/main/common/flatpak-run.c)：同 app 的多个实例共享 per-app tmp；ZaH 仍保存并复查实际目录身份。

## 本轮验证

见 [DEVELOPMENT_VALIDATION](DEVELOPMENT_VALIDATION.md) 与 [REAL_ENV_RESULTS](REAL_ENV_RESULTS.md)。真实Docker/Distrobox已用自制Linux应用验收；不代表SakuraCat或Wine兼容性通过。当前 SakuraCat 和现用代理、网络及生产容器均未操作；没有 Git 写操作或分发打包。
