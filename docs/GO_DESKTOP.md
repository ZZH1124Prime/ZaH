# 桌面主进程、preload 与 Go 联调

这轮实现源代码和开发构建，不制作最终安装包，也不安装系统组件。生产分发仍须完成 Electron Forge、许可证、签名与支持矩阵。管理界面和 Go 的成功构建不等于应用后端兼容验收通过。

## 文件与职责

| 文件 | 职责 |
| --- | --- |
| `desktop/main/index.ts` | Electron 生命周期、窗口策略、IPC 来源校验、系统文件对话框 |
| `desktop/main/security.ts` | 业务方法白名单、输入限额和用户选择路径授权 |
| `desktop/main/manager.ts` | 使用 Go 解析路径，连接或启动 detached manager，读取 fd 3 ready 消息 |
| `desktop/main/rpc-client.ts` | Unix socket 帧、版本握手、请求 ID、超时及有界待处理请求 |
| `desktop/preload/index.ts` | 向 renderer 只暴露 `window.zah`，不暴露 Node、文件系统或原始 IPC |
| `desktop/protocol/static.ts` | 从前端构建目录提供 `zah://app/` 静态资源，拒绝越界、异常链接与不允许的类型 |
| `desktop/shared/bridge.ts` | 桌面桥的 TypeScript 契约 |
| `desktop/tests` | 无 GUI 安全/协议测试，以及可单独运行的真实 Go 联调 |

界面调用 `window.zah.call(method, params)`；Electron 验证主 frame、页面 URL、方法与参数，再通过同一 Go 服务契约调用。`system.info` 中内部路径会在返回 renderer 前移除；renderer 不持有 socket 路径。Go 仍独立校验所有业务参数与权限。

原生选择器包括 `chooseFile()`、`chooseDirectory()`、`saveBundle()`。它们返回路径或用户取消后的 `null`。主进程保留本窗口有限的授权集合（原生选择及后端验证过的当前受管 Bottle 程序候选），材料/迁移文件必须经过文件选择器，共享目录必须经过目录选择器，导出目标必须经过保存对话框。Windows 人工登记同时校验 bottle 目录和 EXE 文件的独立选择。关闭或重新加载页面清除这些授权；已有保存文件不覆盖。

## 整个工作台导出的 IPC 契约

`plan.create` 的 `operation: "workbench.export"` 使用以下输入；它仍先生成计划，用户审核后才通过 `job.submit` 执行：

```json
{
  "path": "/用户选择的目标/workbench.zahbundle",
  "cold_confirmed": true,
  "include_catalog": true,
  "instances": [
    {"id": "linux-instance-id"},
    {
      "id": "windows-instance-id",
      "bottles_archive_path": "/用户选择的来源/full-bottle.tar.gz",
      "bottles_archive_sha256": "64位小写十六进制SHA256"
    }
  ]
}
```

`include_catalog` 是可选布尔值：`true` 请求同时纳入整个库的记录和材料；`false` 或省略时仅纳入所选实例的关联资源。最终内容清单仍由 Go 生成并向用户展示。

主进程逐层拒绝未知字段、非对象条目、重复或非法 ID、空列表以及超过 1000 个实例的请求；冷备份确认必须为布尔值 `true`。目标必须拥有 `saveBundle()` 授权。每个实例还可带 `include_bindings` 已登记绑定 ID 列表。每条可选 Bottles 归档路径必须分别拥有 `chooseFile()` 授权，不能用第一个文件的授权代替其他文件。提供的摘要必须为 64 位小写十六进制，且同时指定对应来源路径；摘要是否必需、归档真实性和资源状态仍由 Go 进一步校验。

窗口最多保留 2048 个路径授权以容纳有界批量选择。请求整体仍受 256 KiB IPC 大小限制。它们只授权用户选择的精确路径，不允许自动扩大到父目录。未确认停止写入、未知来源、缺失后端等情况不会由 Electron 改成成功状态。

## 清理、加密和外部内容选择

`plan.discard` 仅接受 `{id}`，ID 是 32 位小写十六进制。该操作用于退出未提交计划审核，由 Go 保留弃置审计并释放计划引用；已提交任务不能通过它撤销。Electron 拒绝额外 force/path 等字段。

清理操作仍通过计划审核，没有新增通用文件删除接口：

- `instance.forget` 仅接受 `{id}`。
- `instance.data.remove` 接受 `{id,cold_confirmed:true,confirm_private_data:true}`。
- 管理页的 `instance.remove` 必须传 `{id,precheck_private_data:true}`，先检查后续 HOME 清理的已知阻碍；计划和实际删除前各查一次，失败不删除该实例的环境。Windows Bottle 内数据属于环境删除范围。
- `runtime.remove` 接受 `{job_id,instance_id,external_quiet_confirmed:true}`；运行时身份和所有权证据由 Go 从持久任务解析，renderer 不提交任意镜像 ID 或令牌。

上述清理 ID 必须是 Go 生成的 32 位小写十六进制稳定 ID。未知字段、缺失确认、字符串形式的 `"true"` 均拒绝。

`saveBundle(format?)` 仍是原四个桥方法之一：默认或 `"plain"` 选择 `.zahbundle`，`"encrypted"` 选择 `.zahbundle.age`；其他值拒绝。新增计划输入为：

- `bundle.encrypt`：`{path,destination,recipients:string[]}`。来源独立 `chooseFile` 授权，目标 `saveBundle('encrypted')` 授权。只接收 1–128 个不重复、每项不超过 256 字符的 `age1` 公钥字符串；Go 的 age 库进一步核对真实密钥编码。
- `bundle.decrypt`：`{path,destination,identity_path}`。密文与身份文件分别需要文件选择授权，目标来自 `saveBundle('plain')`。IPC 不接受私钥文本字段，身份文件的内容只由 Go 在执行时读取。

`environment.export` 的 `include_bindings` 和 `workbench.export` 每个实例的 `include_bindings` 都是可选的已登记绑定 ID 列表，每列表最多 128 个、不得重复，拒绝任意宿主路径。用户的明确包含选择与 Go 的来源、冷备份及挂载检查共同决定实际归档内容。

## 事件驱动的界面刷新

`events.resume` 的请求只接受 `after` 与 `limit`：`after` 省略/null 或非负安全整数，`limit` 省略或 1–500。返回 `{events,next_cursor,has_more,snapshot?}`；存在快照时替换缓存，正常续读只推进已应用的事件。schema 生成的 `EventPage`、`Event`、`CatalogRecord` 和 `ResumeParams` 位于 `schemas/rpc.generated.ts`。

前端首次读取一致快照，然后按游标分页检查事件，发生变更才刷新相关查询；没有变更时不重复下载整个目录。请求失败保留游标，重连不会自动重提写操作。普通目录写入也产生同事务事件，因此 CLI 或其他客户端的修改可以显示在已打开窗口中。

## 启动和退出

开发构建的 Go 程序为 `bin/zah`，预期将来的分发版本为 `resources/zah`。主进程先调用固定程序的 `paths` 子命令，复用 Go 的 XDG 和备用运行目录逻辑；不读取 shell 配置，也不提供用户拼接的 shell 命令。

已有 manager 通过真实协议握手确认。没有可连接 manager 时，启动 `zah manager --ready-fd 3`，独立会话且 stdout/stderr 不依赖 Electron 管道；专用 fd 3 的有界 ready 消息必须匹配协议和 socket。没有固定 sleep 来猜启动完成。窗口加载与管理器连接并行；连接失败显示实际错误，后续用户操作可以重新建立连接。

正常关窗和退出先经过下文的原生确认与全部停止核实，成功后才关闭客户端连接并退出 Electron；取消或失败保持窗口。Electron 不直接按 PID 向 manager/worker/应用发送终止信号，停止应用由确认后的 Go 任务按精确身份执行。底层连接断开本身不取消任务，manager 根据客户端、任务与恢复状态决定空闲退出。已发送的 RPC 不自动重放；用户重试未知任务提交时复用持久幂等键。

子进程环境保留现用代理变量，移除 Electron 私有库注入与 Node 参数。运行过程中不设置代理、不调整 Docker 服务、不启动容器来探测首页。

## 窗口与资源安全

- 普通用户 Linux x86_64；不添加 `--no-sandbox`。
- `sandbox: true`、`contextIsolation: true`、`nodeIntegration: false`、`webSecurity: true`。
- 禁止新窗口、非应用页面导航、webview 及任意权限申请。
- 静态页面只从 `zah://app/` 提供；生产与开发预览均读取本地构建资源，不启动 Vite 网络服务。
- CSP 禁止远程脚本、连接、frame、对象和表单提交；脚本与样式来自本地构建目录。
- 协议拒绝目录穿越、编码分隔符、越界符号链接、不支持扩展名和超大资源。
- 一帧最多 1 MiB，renderer 请求最多 256 KiB；Node 最多 32 条待处理 RPC，主进程最多 16 条同时受理的界面操作。

依据：[Electron 安全指南](https://www.electronjs.org/docs/latest/tutorial/security)、[BrowserWindow 文档](https://www.electronjs.org/docs/latest/api/browser-window)。

## 开发命令

先将 Go 加入 PATH，或给构建脚本设置 `GO_BINARY` 为 Go 的绝对路径。依赖均锁定在 `package.json` 与 `package-lock.json`，当前开发 Node 22.22.3。

```bash
npm ci
npm run setup:electron
npm run build
npm run test:desktop
npm run test:frontend
npm run test:manager
npm start
```

`setup:electron` 调用已锁定官方 Electron 包提供的 `install-electron` 下载开发运行时，属于开发依赖准备，不是 ZaH 产品安装。Electron 45.2.1 包将运行时下载改为显式/首次使用时获取；单独 `npm ci` 完成并不表示二进制已在本机。

`build` 编译静态 Go linux/amd64、主进程/preload 和前端。`test:desktop` 使用临时文件与假 Unix server，不打开 GUI。`test:manager` 使用真实 `bin/zah`，但所有 XDG 目录在新建私有临时目录中，只验证管理记录和错误，不启动任何应用后端；验证结束观察 manager 自行空闲退出。`npm start` 会打开实际 ZaH 管理窗口，应由用户需要使用时运行；`npm run dev` 先构建再打开同一窗口。

`applications.stopAll` 使用 `plan.create`，外层仅 `{operation:"applications.stopAll",input:{confirmed:true}}`。确认只针对 Go 已登记且核实精确身份的应用；Electron 拒绝额外 PID、容器名、路径和 force 字段，不授予任意进程管理能力。生成类型是 `ApplicationsStopAllInput` 和 `ApplicationsStopAllPlanParams`。前端只需一次批量停止确认，后端继续使用持久计划和任务执行。

## 验收边界

已通过 TypeScript 编译、31 项 Node 安全/协议/关闭协调测试、前端生产构建与 66 项前端测试。`npm run test:manager` 已用真实 Go 二进制通过：专用 ready 管道、自动握手、隔离 SQLite 写入/读取、非法方法/参数失败以及断开后真实空闲退出。当前 Ubuntu 20.04.6 宿主的 Electron 45.2.1 无头联调已通过；实际窗口、中文输入、文件对话框和真实显示会话中的沙箱仍需桌面验收，正式支持矩阵尚未确定。没有通过关闭沙箱绕过兼容问题。

本轮没有进行最终打包/安装、应用真实运行、系统授权、跨机迁移或网络接管测试。

`npm run test:electron` 提供真实 Electron 无头联调入口：使用 headless Ozone、CDP 管道、临时 XDG，删除测试子进程的 DISPLAY/WAYLAND_DISPLAY，不连接用户显示服务；不关闭沙箱。它实际检查 preload 隔离、React 页面、路径授权拒绝、Go 管理器和 SQLite，并在成功时写 `test-results/desktop-smoke.png`。2026-11-01 已在 Z1 / Ubuntu 20.04.6、Electron 45.2.1 实际通过，截图内容来自真实隔离 SQLite 数据。还检查了禁止的 shell 方法、未经过文件选择器的路径请求和 renderer 内部路径隐藏；最新联调覆盖 `events.resume` 初始快照、创建记录后的增量事件、未来游标的快照恢复，以及由事件驱动的应用库更新。迁移页截图为 `test-results/desktop-migration-smoke.png`。首页联调还验证单应用卡、未登记版本时禁用版本选择和运行、唯一全部停止入口，以及取消停止确认后持久任务列表仍为空；未提交任何实际停止。该结果不覆盖真实显示服务、原生对话框、中文输入法或其他应用后端。


## ZaH 窗口关闭

标准窗口边框保留系统最小化、最大化和关闭按钮。X 与正常退出均由 Electron 主进程拦截：原生确认框默认取消，明确提示未保存数据可能丢失。确认后等待已接受的写请求返回，再创建 `applications.stopAll` 计划、以持久幂等键提交并轮询任务；关闭期间允许只读进度查询，拒绝新增写请求。

主进程读取持久任务及其关联计划，要求任务 `succeeded`、计划操作为 `applications.stopAll`、`failed=0`，且冻结目标逐项唯一匹配 `stopped`/`skipped` 结果，才允许退出。renderer 无退出布尔开关，preload 仍只有原来四个方法。取消不提交任务；失败或证据不完整保留窗口。连接错误或两分钟内未完成时保留原任务及幂等键，再点 X 会继续核实，避免重复停止提交；明确失败后的重试创建新计划。如果留窗期间又接受了写操作，重试会先核实原任务，再冻结新批次，防止旧停止结果遗漏后来启动的应用。

关闭状态机用模拟 RPC 验证取消、成功、失败、部分结果、重复 X、未知提交重试、超时续查、错误身份及拒绝写操作。尚未实际确认关闭生产窗口，也未借验收停止真实应用。当前 CUA 没有可用桌面应用，截图仅能来自隔离的无头 Electron，不代表用户实际桌面。

## 停止竞争与断线后的计划恢复

前端根据持久的停止任务及其计划恢复操作锁；`applications.stopAll` 排队、执行或等待安全取消期间，禁止再次批量停止、运行和其他修改。查询与刷新继续工作，任务页仍可请求取消当前批量停止任务；取消完成前不会解除操作锁。

停止任务失败或中断后，如果仍残留 `stop_requested`，界面不再仅据这个标记宣称“正在停止”。只有明确找到终结失败记录、且没有活动停止任务时，才显示状态待确认并提供重新请求停止；仍不允许运行新版本，也不清除后端状态或绕开进程身份核验。

任务页会列出持久计划中尚未看到关联任务的候选项，允许恢复查看及“核实并放弃未提交计划”。它覆盖计划创建应答丢失和窗口重载后的遗留计划。候选判断不是未执行证明；`plan.discard` 在 Go 的互斥与事务内再次检查，已经提交或同时被提交的计划会被拒绝。界面不会自动执行这些恢复出来的计划，也不会在服务拒绝时隐藏它们。
