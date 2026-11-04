# Go 本地通信与基础设施

本文件说明已经实现的协议和文件边界。业务方法与任务状态见 Go 核心说明；这里的握手成功只证明管理器连接可用，不证明 Docker、Distrobox 或 Bottles 可用。

## 数据流

Electron main 或 CLI → Unix socket → 长度前缀解析 → 对端 UID 校验 → `protocol.hello` → service Handler → JSON-RPC 响应。renderer 不直接访问 socket。事件续读由业务 `events.read` 方法使用持久序号完成，传输层不自行缓存事件。单次最多返回 500 条事件；目前保留全部持久事件，不裁剪历史，worker 日志回收也不删除事件。

`schemas/rpc.json` 是协议信封及限额的来源。执行 `go generate ./internal/transport` 生成 `internal/transport/wire_generated.go` 与 `schemas/rpc.generated.ts`。业务方法的参数校验仍由 service 完成；这些信封类型不代表已完成 Electron IPC/preload。

## 帧与握手

每帧为 **4 字节无符号大端长度 + UTF-8 JSON 字节**。长度包含 JSON 字节，不包含前缀；合法范围为 1–1,048,576 字节。支持 JSON-RPC 2.0 单请求、单响应和无 `id` 通知，不支持 batch。通知不返回响应，修改操作应使用带 ID 的请求和业务幂等键，以便获得受理结果。

连接的第一条消息必须是有 ID 的：

```json
{"jsonrpc":"2.0","id":1,"method":"protocol.hello","params":{"version":1}}
```

响应：

```json
{"jsonrpc":"2.0","id":1,"result":{"version":1,"max_frame_bytes":1048576,"max_connections":32}}
```

缺失握手、通知握手或不兼容版本返回 `-32001`（通知无响应）并断开。每条连接顺序执行请求，最多 32 条已接受连接；不再接纳超额连接。默认读、写和业务上下文期限为 30 秒，`Server.RequestTimeout` 可用于本机测试调整。耗时业务必须返回持久任务 ID，交由 worker 执行；请求上下文取消不意味着可以安全中断安装器。Handler 必须遵守 context；Go 无法安全强制终止不响应取消的函数。

错误码：

| 码 | 含义 |
| --- | --- |
| -32700 | JSON 解析失败 |
| -32600 | 请求信封不符合契约 |
| -32601 | 未知业务方法（service 返回） |
| -32602 | 业务参数无效（service 返回） |
| -32603 | 内部错误或编码失败，具体敏感错误不透传 |
| -32001 | 握手或协议版本不匹配 |
| -32002 | 请求超时或取消 |
| -32003 | 响应超出帧上限；调用方应分页 |

帧超长、空帧、截断连接直接关闭，不尝试为超大内容分配内存。不把普通 Go error 原文发给客户端，以免泄漏凭据。可公开错误使用 `*transport.Error`；其 `Data` 仍须业务层脱敏。

## Go 调用接口

- `transport.Serve(ctx, socket, handler)`：服务直到 ctx 取消。
- `transport.Server{Handler, OnReady, OnConnect, OnDisconnect, RequestTimeout}.Serve(...)`：带生命周期回调；回调可能并发，必须自行同步。`OnReady` 在监听建立、权限设置完成后发生，可向启动方发送 ready 消息。空闲退出由 manager 结合连接计数、队列和 worker 状态决定。
- `transport.Call(ctx, socket, method, params, result)`：每次建立本地连接、自动握手、完成一次调用；无自动写请求重试。
- `transport.Handler`：`func(context.Context, string, json.RawMessage) (any, error)`。

双方通过 Linux `SO_PEERCRED` 核对 UID。socket 目录须本用户所有且无组/其他用户权限，socket 为 0600。服务自己持有 `manager.sock.lock`，已有非 socket 文件、链接或其他用户对象一律拒绝覆盖。退出时移除监听 socket，锁文件保留以避免 inode 替换竞态。

## 路径和锁

`infra.Resolve()` 不创建文件；`Paths.Ensure()` 创建并验证 Config/Data/State/Cache/Runtime。遵守技术规格的 XDG 默认目录；配置的 XDG 路径必须为绝对路径。现有目录不自动 chmod，而是对权限、所有者、链接失败明确报错。

有效的 `XDG_RUNTIME_DIR` 使用其中 `zah` 目录。缺失或不安全时采用 `$XDG_STATE_HOME/zah/runtime`（默认 `~/.local/state/zah/runtime`）作为用户私有备用 IPC 目录：所有入口可确定同一路径，不使用可能被其他用户抢占的固定 `/tmp` socket。该备用目录中的 socket/锁是临时运行状态，但目录可保留。Unix socket 路径限定 100 字节；过长配置明确报错，不静默换到另一管理器。

`infra.AcquireLock(path)` 使用 Linux `flock`，立即成功或返回 `ErrLocked`；锁文件为普通单链接文件且属于本用户，拒绝符号链接与硬链接。`Lock.Close()` 释放锁。`AcquireLocks(ctx, dir, names...)` 去重并按字典序获取，失败释放本轮部分锁，等待时响应取消；调用方应将全局修改锁与环境 ID 锁一起纳入集合。锁只能约束遵守协议的 ZaH 进程，不能替代上游实际状态检查。

## 已执行验证

在隔离临时目录运行 `go test -race ./internal/infra ./internal/transport`：

- XDG 默认值、相对路径拒绝、私有目录权限、链接拒绝；
- 锁竞争、取消释放、去重顺序、非法名称、锁文件软/硬链接拒绝；
- 并发客户端、正确握手、错误握手、通知无响应、关闭服务和 socket 清理；
- 帧上限、截断输入、无效 JSON/ID、响应上限、内部错误脱敏和 panic 恢复；
- 已有普通文件不被 socket 服务覆盖。

没有修改代理、网络、Docker 或其他正在运行的服务。跨 UID 拒绝有生产检查代码，但本轮未创建第二用户进行实测。Electron 的 IPC 来源验证、preload 白名单及完整桌面性能不属于此模块的已测结果。

## 事件续读与一致快照

`events.read` 保留旧接口兼容。新客户端调用 `events.resume`，参数由 schema 的 `ResumeParams` 生成：

```json
{"after": 42, "limit": 100}
```

`after` 可省略或为 `null`，表示初始快照；显式 `0` 表示从第一条事件开始续读。有效游标是 0–9007199254740991 的整数，避免 JavaScript 精度丢失。`limit` 可省略，默认 100，显式值必须是 1–500 的整数；`null` 和 0 不代替默认值。

响应是生成的 `EventPage`：

```json
{"events": [], "next_cursor": 42, "has_more": false}
```

初始读取、超出最新序号的游标或检测到历史缺口时，响应额外包含 `snapshot`；它表示客户端应替换全量缓存。快照、游标和事件在同一 SQLite 读事务中采集。正常分页的 `next_cursor` 只推进到实际返回的最后一条事件；空页保持原游标，`has_more` 为 true 时继续下一页，不能直接跳到服务端最新序号。

快照含 family、release、instance、environment、artifact、binding、capability、job 八种记录。recipe 和 plan 通过各自 catalog 查询加载，并在快照重置或相应变更事件后刷新。`record.changed` 与实际记录写入共享数据库事务，避免服务在修改提交后、写事件之前崩溃造成不可见变更。客户端请求失败保留已应用游标；恢复连接只重放读取，不能因此自动重试写任务。

窗口仍保持现有四个 preload 方法；事件页通过 allowlisted `call('events.resume', params)` 读取，不增加 renderer socket、任意文件能力或 TCP 服务。完整帧限制仍为 1 MiB，事件分页不解除输出预算。

## 安装恢复操作

`plan.create({operation:"instance.verify",input:{id:"<32位实例ID>"}})` → 审核计划 → `job.submit`。这是显式核实人工修复后的 Linux 安装入口，约束和状态见 [GO_CORE](GO_CORE.md)。它不属于同步 RPC 的30秒请求；容器首次初始化在持久 worker 中运行。


## 安装引导与桌面契约

`system.info.desktop_contract=3` 表示当前桌面依赖的安装引导契约可用；Electron 在已有 socket 上握手后核对它。旧 manager 仅断开这个客户端并显示升级等待提示，不启动竞争 manager、不自动停止活跃服务。用户重构建前应正常关闭 ZaH；旧服务无任务/客户端后约30秒自动退出。

| 方法/计划 | 输入与结果 | 写入/执行边界 |
| --- | --- | --- |
| `installation.inspect` | artifact_id → 格式、平台、名称/版本提示、候选入口 | 只读已归档材料；不会执行安装包 |
| `installation.options` | platform → 本地基础镜像/runner 选项 | 显式上游列表查询；不拉镜像、不安装运行时 |
| `installation.configure` | request_id、artifact_id、可选 family_id、name、version、base、entry、mode → instance | 同一请求可安全重试；一次事务保存完整目录关系，不执行安装 |
| `installation.programs` | id → 该环境内的 x86/AMD64 EXE 或 Linux 安装后候选 | Windows 读取官方 programs；Linux 读取容器内 desktop/AppImage；不扫描宿主其他目录 |
| `plan.create: installation.finish` | id、executable_path、confirmed:true | 冻结入口摘要；worker 证明环境停止后保存应用入口 |
| `plan.create: installation.maintenance` | id、confirmed:true | 停止证明后切回保留安装器；不启动安装窗口 |

renderer 只能选择已验证的受管候选或原生文件对话框选中的 EXE；服务仍独立检查它属于准确 Bottle。`manager.retry` 仅为 Electron 本地控制方法，不是 Go RPC 方法；仅接受空参数。IPC 预期错误使用 `{ok:false,error}` 信封，preload 仍向 React 返回拒绝的 Promise，不暴露通用 IPC。

### 图形安装入口确认（2026-11-02）

`installation.configure` 可选 `dpi`（Windows：0 使用 Wine 默认，或 96..384）；EXE 按归档原名保存，ZIP 保留目录树。Linux 不再从包内候选自动确定最终入口。准备完毕后 stage 为 `installer_ready` / `awaiting_completion`，仅 `installation.finish` 可标记完成。

`installation.programs` 返回候选，绝不自动选择；Linux 含 `kind/path/cwd/sha256`，Windows 含 `path/cwd/args`。`installation.finish` 输入 `id/executable_path/confirmed`，可附 `kind/cwd/args`；Linux摘要由计划阶段重新读取，renderer不可提交身份摘要。Windows需精确停止，Linux需无应用启动租约并有运行中的已准备容器。Windows保存 Bottle 内相对路径和字面参数，通过专属名字调用公开 CLI；不用发现列表临时 UUID。公共 CLI 的工作目录限定为 EXE 父目录，其他目录明确拒绝。

`installation.tools` 是计划操作，输入 `id/action`；Windows只允许 `files/uninstall`，Linux只允许 `start/files/install/packages`（start显式启动已登记容器，候选查询本身不主动启动）。各自使用已有原生工具，不解释任意命令；Linux缺 thunar/gdebi-gtk/synaptic 时明确提示。`installation.location` 仅供主进程按已登记 Windows 实例取得文件选择框起点，不向 renderer 开放任意路径查询。
