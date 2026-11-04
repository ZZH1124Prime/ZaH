# 可恢复清理记录

日期：20261101T101756Z

仓库外备份：`/home/zzh/Documents/Codex/2026-11-01/task/zah-cleanup-backup-20261101T101756Z`。逐文件原始大小、SHA256和操作在备份的 `manifest.json`；删除前已校验备份字节一致。恢复时按清单选择单个文件复制回原路径，不执行Git reset。

## 移出内容

- 旧Python扩展与验收文档、SakuraCat历史部署长文：对应的原型源码早已移出，不能作为新版操作指南。安全限制仍保留在技术规格、BACKENDS及真实验收方案。
- PROJECT_STRUCTURE：内容仍宣称main.go和RPC未实现，当前结构已在README及模块文档准确说明。
- GO_MODULES：106KB逐函数/行号教学索引，和现有模块说明重复且易失效；用户已改为按模块功能了解，原稿保存在备份。
- 已有源码的目录及未使用features目录中的.gitkeep：纯骨架占位，不是产品功能；recipes/release的必要预留保留。

未发现可在不影响生产能力和回归覆盖的前提下确认无用的Go内部函数，因此没有为追求删除数量而删业务代码或测试。需求、API、迁移、兼容、日志、安全和许可证/SBOM均保留。

移出文件共 29 个：

- `docs/EXTENDING.md`
- `docs/VALIDATION.md`
- `docs/Skills_Ubuntu_SakuraCatContainer.md`
- `docs/PROJECT_STRUCTURE.md`
- `docs/GO_MODULES.md`
- `tests/.gitkeep`
- `desktop/main/.gitkeep`
- `desktop/protocol/.gitkeep`
- `desktop/preload/.gitkeep`
- `frontend/src/components/ui/.gitkeep`
- `frontend/src/features/.gitkeep`
- `frontend/src/app/.gitkeep`
- `frontend/src/lib/.gitkeep`
- `internal/service/.gitkeep`
- `internal/catalog/.gitkeep`
- `internal/backend/.gitkeep`
- `internal/host/.gitkeep`
- `internal/infra/.gitkeep`
- `internal/jobs/.gitkeep`
- `internal/recipe/.gitkeep`
- `internal/transport/.gitkeep`
- `internal/domain/.gitkeep`
- `internal/migration/.gitkeep`
- `internal/storage/.gitkeep`
- `internal/bootstrap/.gitkeep`
- `internal/process/.gitkeep`
- `schemas/.gitkeep`
- `cmd/zah/.gitkeep`
- `scripts/.gitkeep`

另将 DEVELOPMENT_VALIDATION 中重复的阶段结果合并为现行验收记录，保留唯一能力/边界说明。合并前文档及随实测更新的README/就绪说明/后端说明已备份至上述备份目录的 `documentation-before-results/`，附独立SHA256清单；原始29个移出文件清单不变。

## 2026-11-02：收敛到三页正常使用流程

本轮备份与精确差异位于 `/home/zzh/Documents/Codex/2026-11-01/task/simplification-20261102/`。`source-before.tar.gz` 保存修改前 1767 个未忽略源码/文档文件，`before-manifest.json` 保存 SHA256 和本轮开始前已经缺失的 39 个文件；这些既有删除不是本轮造成。只需从归档选择原文件恢复，不要覆盖整仓或运行数据。

本轮没有删除整个源码文件、新增产品页面或测试脚本，而是在现有文件中移除无用代码：

- `frontend/src/lib/api.ts`：删除无消费者的配方解析、旧镜像清理候选、格式化字节数、操作名称映射、旧停止结果解析；桥接类型复用 `desktop/shared/bridge.ts`，不维护两份声明。Go 兼容 API 和安全校验保留。
- `InstallationGuide.tsx`：移除没人调用的“在安装过程中另建/选择应用”路径，始终使用卡片传入的应用。保留必要版本、入口、基础环境/runner 选择和提交幂等性。
- `ApplicationLibrary.tsx`：移除恒为 false 的 `submissionPending` 和重复状态分支。`App.tsx` 的启动/停止状态改为同一次任务扫描，不重复转换计划和逐项查找。
- `DependencyInstall.tsx`：复用现有取消按钮；等待任务和依赖安装共用现有执行中状态集合，不添加新的任务框架。
- `HostPanel.tsx`：按 Go 实际宿主报告字段读取，移除从未存在的大小写双格式猜测。后端协议未变。
- `primitives.tsx` / `styles.css`：移除无人使用的 JSON 详情控件及 91 个旧任务、配方、绑定、复杂管理页面的样式选择器；当前三页样式保留。
- `internal/storage/store.go`：删除没有生产消费者的 `AddReference`、`RemoveReference`、`AppendEvent`。现有测试改用生产实际使用的事务记录/引用接口，事件仍随记录事务生成，数据库结构和安装记录不变。

验证仅运行已有前端 54 项、桌面 41 项、Go 存储/服务测试，以及 Go、桌面、前端类型/构建检查；全部通过。前端从 57 项变为 54 项，是删除配方解析和旧镜像清理候选的 3 项孤立辅助函数测试，正常安装、取消、身份、路径与幂等断言保留。未新增测试框架、压力测试或重新跑真实后端。

三页需求全部保留。删除权限、异常安装恢复、共享目录重绑定与未知外部结果处理本轮不扩展，留待实际安装测试后决定。用户活动构建保持不变，未操作运行数据、系统组件、代理网络、生产容器或 Git。
