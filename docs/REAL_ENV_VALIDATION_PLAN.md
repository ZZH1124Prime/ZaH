# 实环境最小验收方案与授权范围

2026-11-01：以下盘点最初仅只读完成。随后用户明确选择在Z1执行Linux方案，接受已披露的Distrobox权限、最多三个测试容器、容器内官方依赖及精确清理；实际结果单独记录。Windows系统依赖安装仍未授权。不得更改代理、网络、Docker配置或操作现用应用。

## 已核实的本机条件

Docker 26.1.3、overlay2、数据根 `/var/lib/docker`；现有本地镜像均为 linux/amd64：

| 标签 | 完整本地 image ID | 可固定的 RepoDigest | 大小 |
| --- | --- | --- | --- |
| ubuntu:22.04 | sha256:a7b0f6af325461862a99c677cf3229ca5241d9f4f4289bd00cc2cce91a3aff23 | ubuntu@sha256:076e7bf82319619f28dd0aa9c02ab34b055ad37100395b4f49a2ffba04fb5e11 | 78,096,818 B |
| ubuntu:24.04 | sha256:d9b8c34d61a9b9a3c44cab883d7d2f2619d674966ab520186f799cf32e3e0377 | ubuntu@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852 | 78,181,941 B |
| ubuntu:26.04 | sha256:febc1a2eec1fdaad5db7e542ce1fefaee9656b6e3a177f0c703d616e77b86a62 | ubuntu@sha256:6a06111c3b0a8600feadb270aec771dc239bf407b25625f93f44ab00e11f4d3c | 101,518,381 B |

本机已有 gcc、pkg-config、libX11 开发文件1.6.9和 dpkg-deb，Go工具链也已具备。未发现项目 recipes/tests 目录中的非代理真实安装材料；没有扫描用户下载目录或应用配置。现有测试中的惰性文件并不是可以安装运行的真实应用。

可自行编写**不使用网络、不读真实数据**的测试程序：静态 Go CLI 写心跳和停止标记；C/Xlib 窗口显示版本、提供写入测试文件按钮、正常退出/长驻/派生子进程模式。用现有工具生成无维护脚本的测试 `.deb`，安装到容器 `/usr/bin/zah-qa-app`。这只是验收夹具，不是 ZaH 分发安装包。已在隔离验收目录生成，实际执行结果见验收记录。

它可验证材料归档、安装、GUI显示、数据保存、版本互斥、不同应用并行、精确单停、全部停止及 ZaH 关闭流程；不能证明 SakuraCat 的登录、代理、托盘、系统网络接管、真实 AppImage 自解包或其他商业应用兼容性。CLI单独通过也不能证明GUI兼容。

## 必须明确告知的 Distrobox 权限事实

当前产品调用本机 Distrobox 1.8.2.5 默认 create 路径，没有额外收紧 namespace。实际脚本 `/home/zzh/.local/bin/distrobox-create:692` 起包含 `--privileged`、`apparmor=unconfined`、共享 host network/PID/IPC，并把 `/` 挂到 `/run/host`（Docker分支约767行）。自定义 HOME 不等于安全沙箱。官方也将其定义为与宿主紧密集成的容器：[Distrobox create](https://distrobox.it/usage/distrobox-create/)。

这不会要求我们改宿主防火墙/DNS/路由或 Docker daemon 配置，也无需新建 Docker bridge 网络，但会创建一个权限很高、共享宿主网络命名空间的新容器。不能把它描述成“只写隔离目录、完全不接触宿主权限”。本次没有执行该创建。

Ubuntu基础镜像首次进入还可能运行容器内 apt update/install，安装 Distrobox 自身依赖和测试窗口的 libX11；已在 `distrobox-init:765–880` 核对。不会安装宿主软件，但会产生下载流量。不能承诺“本地有镜像所以全程离线”。不修改现有代理变量；连接失败就停在该处，不更换代理、DNS或源。

**若不能接受此新容器权限，建议在独立测试机器/已有VM进行真实 Distrobox 验收。**原始 Docker `--network=none --cap-drop=ALL` 测试只能验证部分进程行为，不能冒充当前 Distrobox 产品路径验收；不为测试私自换技术路线或改变默认实现。

## 可一次批准的 Linux 范围 L

仅当用户明确接受上述新容器权限事实后执行：

- 固定使用已存在的 Ubuntu22.04 RepoDigest及上述image ID，不 pull、不修改/删除现有镜像。
- 编译自有无网络CLI/Xlib测试程序，生成两个版本的测试deb；不安装到宿主、不借用 SakuraCat。
- 创建应用A两个版本、应用B一个版本，共最多3个新容器；同时最多2个测试应用运行。容器名由真实新实例ID生成 `zah-<新UUID>`，创建前记录三组完整ID/名称/`org.zah.instance`标签；绝不按名称前缀批量删除。
- 独立根目录 `/home/zzh/Documents/Codex/2026-11-01/task/zah-acceptance-<本轮随机ID>/`；XDG数据/配置/状态/缓存都指向该根；私有HOME为 `data/zah/instances/<实例ID>/home`。Unix socket运行目录单独使用短路径 `/tmp/zah-qa-<随机ID>/run/zah`，0700，避免socket路径过长。无真实目录绑定。
- 顺序初始化容器；允许仅在这三个新容器中从其Ubuntu官方软件源下载和安装Distrobox必要包及libX11；不在宿主apt install、不改源/代理。若需超出这些依赖、权限修复或网络调整，停止并报告。
- 测试A1运行时A2被拒绝；B可同时运行；单停A时B心跳继续；切换到A2；全部停止只作用于此隔离目录登记的目标；测试窗口关闭后卡片仍能停止环境；ZaH原生关闭取消不停止，确认后逐项停止并退出。
- 显示最多一个ZaH测试窗口及两个自有测试窗口；不选择、关闭或覆盖用户现用SakuraCat/ChatGPT窗口。不使用xhost +、不改变显示服务器访问控制。
- 规划预留约8GiB额外磁盘，预计轻量窗口阶段内存低于1GiB，首次apt初始化峰值未知；这是预算估计而非当前实现的硬配额。现有磁盘可用约473GB（十进制）。若发现资源异常，停止后续步骤，不强杀未知事务。
- 清理授权可一并限定：测试完成后仅停止并删除本轮账本中身份匹配的三个新容器，再移除该本轮私有数据目录及短runtime目录；保留验收记录/截图/摘要。删除前再核实容器已停止和身份未漂移。无 `prune`、无镜像删除、无通配容器操作。若用户不授权删除，则保留停止状态和清理账本供复查。

如用户只批准“运行测试”而未明确批准新Distrobox权限或本轮资源删除，不自动扩大权限/删除范围。现场若显示授权需要交互，等待用户，不绕过。

## Windows 范围 W：优先另一台已具备依赖的测试机

本机 `flatpak` 为 not-installed。已有 bubblewrap0.4.0、xdg-desktop-portal1.6.0、portal-gtk1.6.0；这不代表当前Bottles运行时兼容已经验证。

官方首选发行源：[Bottles安装文档](https://docs.usebottles.com/getting-started/installation)、[Flathub com.usebottles.bottles manifest](https://github.com/flathub/com.usebottles.bottles)。当前manifest使用GNOME50/Wine base stable-25.08及多种扩展，并声明网络、显示、音频及设备访问；它不是纯离线小包。启动后runner等组件还可能从[Bottles官方components](https://github.com/bottlesdevs/components)下载，见[首次运行文档](https://docs.usebottles.com/getting-started/first-run)。不以旧文档70MB数字承诺当前总下载量。

有Flatpak基础程序之后，应用/运行时和remote可采用 `--user`，写入用户目录，参考[Flathub用户级安装](https://docs.flathub.org/docs/for-users/user-vs-system-install)。**`--user`不能补上本机缺失的Flatpak系统程序。**[官方Ubuntu安装步骤](https://flathub.org/en/setup/Ubuntu)要求系统apt安装Flatpak；其重启建议不在本轮自动授权内，不会重启机器或当前会话。

只读 `apt-get -s --no-install-recommends install flatpak` 当前缓存结果为新增3包、无升级/删除：

- flatpak 1.6.5-0ubuntu0.5
- libappstream-glib8 0.7.16-1ubuntu2
- libostree-1-1 2020.3-1

实际安装可能触发系统包维护脚本、D-Bus服务/系统文件登记；结果受索引时效影响。当前没有证明该旧Flatpak可支持当期全部运行时与权限选项。因此不建议仅凭这张模拟清单直接批准一个承诺成功的Windows闭环；若在Z1推进，应先批准仅这三包的安装及用户级remote准备，任何事务变化、要求新源/升级/重启都再次停止核实，不偷偷扩范围。

更小的风险选择：由用户提供另一台已安装兼容Flatpak/Bottles的测试Linux机器。W验收只新建两个无账户数据的受管Bottle，显式普通runner/direct Wine设置；用自有无网络Windows测试EXE，验证阻塞/延迟启动、单停A而B继续、失败重试、全部停止。只核实固定prefix/runner，不调用Bottles全局kill。资源规划预留10GiB磁盘（运行时/扩展实际下载和大小待安装前清单确认）。清理仅本轮两个Bottle，运行时/remote/系统包默认保留，不全局卸载unused。

## 父对话可提供的有限选项

1. **L：只批准Z1上的上述Linux完整测试**，明确接受新增Distrobox默认权限、容器内官方包下载及本轮精确清理；Windows继续未验证。
2. **L/W：提供独立测试机器或已有VM**，在该处执行同样的Linux和双Bottle方案，避免现用代理宿主成为高权限测试环境。创建新VM或安装其系统也需要另定范围，不能默认已批准。
3. **暂不批准真实执行**，继续保留源码/静态验收结果，不宣称真实可用安装版已就绪。Z1安装Flatpak须作为单独明确的系统变更决定，不能藏在“批准测试”里。

## 不需要真实执行许可的准备已完成

已用现有文件离线生成 `docs/licenses/npm-source.cdx.json`（210组件），以及现有Go开发二进制11个链接模块的 `go-development-build.json` 和 `DEPENDENCIES.md`。没有新装SBOM工具、没有重建、没有生成安装包。它们用于打包前整理，不替代最终实际分发文件的SBOM/NOTICE；项目自身许可仍由所有者决定。
