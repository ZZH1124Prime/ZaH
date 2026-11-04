# ZaH：Linux 应用运行与迁移工作台

ZaH 面向 Linux 桌面，计划统一管理 Linux 与 Windows 应用的安装材料、运行环境、多版本实例、宿主文件访问及备份迁移。产品显示名为 **ZaH**，程序和文件系统标识使用小写 `zah`。

> **当前状态：开发态实现与集成验收。** Go 管理核心、Electron 安全桥接和 React 工作台已有源码与开发构建入口。真实应用兼容、授权与跨机器恢复仍须单独验收；不把模块测试当作兼容认证。Ubuntu 20.04 amd64 用户级安装包已完成构建、安装与真实停止验收，见 [安装说明](release/README.md)。这不代表所有 Windows 应用兼容或另一台全新机器已完成验收。旧 Python 原型文档已移入仓库外备份，见 [清理记录](docs/CLEANUP.md)。

## 技术栈

| 层次 | 实现/分发方案 |
| --- | --- |
| 桌面 | Electron，负责窗口、系统对话框及 Go 连接 |
| 界面 | React + TypeScript + Vite，配合 Tailwind、本地 UI 控件、Lucide、React Router 与 TanStack Query |
| 核心 | Go，集中处理 CLI、任务、材料、运行后端和数据管理 |
| 存储 | SQLite，由 Go 访问，`database/sql` + `modernc.org/sqlite` |
| Linux 应用后端 | Distrobox + Docker |
| Windows 应用后端 | Flatpak 版 Bottles + 经验证的 Wine runner，在 Linux 宿主运行 Windows 应用 |
| 分发 | 自解压用户级安装包组装版本化目录，Electron、前端资源与 Go 程序随包提供 |

管理界面已通过隔离无头联调，并已在三个真实Linux测试容器上完成自制deb/X11运行与停止闭环，见 [实环境结果](docs/REAL_ENV_RESULTS.md)；第三方应用兼容和最终分发支持范围仍需实测。具体应用运行时先检查并准备对应后端。

## 目录与实现状态

```text
zah/
├── desktop/                  # Electron main、preload、本地资源协议
├── frontend/src/             # React 页面、组件与客户端
├── cmd/zah/                  # Go CLI / manager / worker 入口
├── internal/                 # Go 领域、业务、后端、任务和存储等模块
├── recipes/                  # 应用运行配方（预留）
├── schemas/                  # RPC规范及生成的Go/TypeScript契约
├── scripts/                  # 开发构建和隔离验证脚本
├── release/                  # 打包脚本与安装说明（生成的安装包不提交）
├── go.mod                    # 已创建的 Go 模块声明
├── tests/                    # 跨进程契约、集成与验收测试
└── docs/                     # 技术规格、结构说明与原型资料
```

运行入口为 `cmd/zah/main.go`、`desktop/main/index.ts` 和 `frontend/src/main.tsx`。`go.mod/go.sum`、`package.json/package-lock.json` 固定实际依赖；空目录中的 `.gitkeep` 不代表功能已实现。

## 开发入口

```bash
npm ci
npm run setup:electron
npm run build
npm test
go test ./...
npm run test:manager
npm run dev
```

`setup:electron` 下载官方 Electron 开发运行时，`build` 生成项目内开发产物，不制作安装包。`dev` 会打开开发窗口；它不自动安装后端或启动应用。普通安装由卡片操作自动串联，进度和错误显示在当前界面；删除、全部停止及关闭 ZaH 使用确认对话框。开发依赖需 Node 22.22.2 及以上和 Go 1.27.3；发行版用户无需这些工具链的目标在最终打包阶段验证。

源码修改后，本机通常只需先正常关闭 ZaH、停止其受管应用，再运行：

```bash
cd /home/zzh/zah
npm run dev
```

`dev` 会完整构建 Go、Electron TypeScript 和 React，再启动窗口；没有热更新。已有依赖不必重复 `npm ci`。本机入口会自动找到已安装的 Go 1.27.3 和已验证私有 Windows 依赖，不再需要手写 `GO_BINARY`。`npm start` 只启动已有构建；`npm run check:runtime` 只校验依赖配置，不构建、不打开窗口。若已有 manager 仍运行，启动入口会拒绝覆盖构建；请正常关闭原窗口并等待约 30 秒，不强杀任务或删除数据库。

日常安装：在“应用库”添加名称与平台，再点击该卡片上的加号选择安装包。确认版本和本地基础环境后开始准备；安装后的应用入口必须由用户在图形界面选择确认。Linux 引导支持 amd64/all deb、x86_64 type-2 AppImage，不要求手填镜像摘要或配方。Windows 单 EXE 选入或拖到应用卡片后自动准备环境并打开，不要求区分安装器和便携程序；版本缺少元数据时使用导入时间，缩放默认跟随当前显示比例。ZIP 保留完整目录，让用户选择其中要打开的 EXE；原生窗口由 Wine 显示、由用户操作。完成后停止这个环境，在“安装与入口”选择最终程序并保存，以后从卡片运行。ZIP 保留完整目录及依赖。

“管理”提供依赖检查、明确确认后的基础依赖安装及应用／版本的单删、多删、全选删除；基础 APT 安装支持 Ubuntu 20.04/22.04/24.04、Debian 12/13 的现有 APT 软件源，不添加源、自动升级或删除软件，不自动配置服务权限；Ubuntu 20.04 的 Windows 分支复用私有 Flatpak/Bottles/Soda 准备命令，缺少宿主依赖或下载失败时显示日志及手动命令。软件包自身安装脚本可能启动服务等，确认窗口会说明影响。

Z1 的 Ubuntu 20.04.6 不使用系统源中的旧 Flatpak 1.6.5。私有 Flatpak 1.18.4 已在固定用户目录重建，Bottles 67.5、GNOME 50、Soda 11.0-10 已准备。此前独立测试配置已完成自制 Win64 GUI 的安装、并行、停止和同机恢复；当前固定 profile 尚未复跑双 GUI。开发入口自动发现 `~/.local/share/zah-focal-desktop/profile.json`，仅给 Bottles 子进程使用临时 D-Bus，不改变桌面会话。启动不下载或升级依赖；Bottles 自身可能刷新在线目录。固定路径不可直接搬迁，这仍是开发配置，非可分发安装包。见 [20.04 固定依赖路线](docs/compatibility/FOCAL_RUNTIME.md)。

“迁移”按应用导出，校验后按稳定 ID 合并：同平台、架构、版本跳过，缺少版本加入，独立创建的同名应用不合并。环境数据恢复到系统选择器指定目录下的新私有子目录。导入后自动逐个重建受支持的环境，只有入口、身份及停止状态校验成功才允许运行。失败项保留数据，卡片提供“恢复环境”重试。Linux 使用新 Distrobox 和已校验镜像；Windows 使用新 Bottle、已安装的同版本 runner 和可移植恢复配置。旧 Bottle 包、跨用户名和特殊配置不自动转换。**恢复流程已通过隔离夹具，Windows 自制 GUI 的真实同机备份、指定目录恢复和重新运行也已通过；跨机器/用户名和第三方应用尚未验证。** 不会把已检查归档说成已恢复，也不会因失败移除仍存在的应用记录。完整边界见 [就绪清单](docs/RELEASE_READINESS.md)。

`bin/zah doctor` 只读检查路径，不联系 Docker；`bin/zah rpc` 会按需启动本用户 manager 并创建管理数据。不要在真实应用数据目录运行自动化测试。当前没有执行系统安装、宿主菜单安装或发布。

模块功能与数据流先看 [模块与就绪清单](docs/RELEASE_READINESS.md)，技术细节见 [Go 核心](docs/GO_CORE.md)、[Electron 层](docs/GO_DESKTOP.md) 和 [React 层](frontend/README.md)。验收结果与未验证范围见 [开发态验收](docs/DEVELOPMENT_VALIDATION.md)，进入安装包制作前的门槛见 [就绪清单](docs/RELEASE_READINESS.md)。

项目源码不要求固定的个人绝对路径；当前私有原生依赖按本机前缀编译，移动源码后须重新生成对应开发启动适配器。旧原型的 `run.py`、`src/vpn_workbench/`、专用配置与测试已不在当前源码树中。

## 文档导航

- [技术规格与实施路线](docs/TECHNICAL_ROADMAP.md)：目标行为、架构、数据、迁移及验收条件。
- [2026-11-02 质量审查](docs/QUALITY_AUDIT_2026-11-02.md)：本轮修复、可精简点、真实验证及尚未实现的普通用户流程。

当前重点是简化正常安装、版本管理和迁移流程，具体兼容性与权限问题以用户实际安装验证为准。材料备份、运行环境备份和真实应用恢复是不同能力，界面按实际范围标明；历史原型经验不作为新版验收证据。

## 版本管理与本地文件

`.gitignore` 采用通用、保守规则，排除依赖目录、明确的构建输出、缓存、覆盖率报告、日志、临时文件、本地环境配置、安装材料与迁移包。项目内临时开发数据（包括 SQLite 数据库）约定放在 `.dev-data/`；正式运行数据按技术规格使用用户的 XDG 目录。

源码、`release/` 打包配置、`recipes/`、`schemas/`、公开配置样例与 `.gitkeep` 应保留。实际产生的 `package-lock.json` 等依赖锁文件和 `go.sum` 应纳入版本管理；不全局忽略 JSON 或数据库文件，以免隐藏配方和测试夹具。环境配置示例可命名为 `.env.example`、`.env.sample` 或 `.env.template`，内容只能包含占位值。

后续工具产生新的中间文件时，再按实际路径补充规则。忽略规则不会自动移除已跟踪的文件，提交前仍需检查内容。Git 提交与上传由项目所有者自行管理；模块路径已与仓库地址对应，许可证仍待所有者确定。

## 清理记录

过时骨架说明、Python原型教程及逐函数教学索引已移至仓库外备份；需求、安全边界、当前模块和测试均保留。清单与恢复位置见 [CLEANUP](docs/CLEANUP.md)。
