# Go 材料库与迁移容器

这两个包实现本地字节存储和归档验证，不启动 Docker、Bottles 或安装器。应用运行环境的冷备份、ID 重写、数据库登记和恢复验证由 service/backend 承担；导入文件成功不能标为应用恢复成功。

## 材料库：internal/catalog

`Store.Root` 是绝对路径 `<data>/artifacts/sha256`，目录必须为私有目录（0700）。`Import(ctx, source)` 只接收普通文件，拒绝来源末级软链接和设备/FIFO；建立独立临时副本，流式 SHA256，重新读取副本验证，最后以同文件系统无覆盖原子发布。最终文件名为 64 位小写摘要。临时副本与最终对象短暂硬链接用于无覆盖提交；**从不把用户原文件硬链接进材料库**。默认最大材料 32 GiB，可通过 Store.MaxBytes 配置，最高实现边界 1 PiB。

同内容去重不丢弃来源：每次 Import 都返回新的 Source（原名、原路径、导入时间）。调用者必须将它追加进数据库追溯记录。平台/架构等未知字段保留 unknown；版本、Origin、Signature 未知时为空，不推断可信来源。只读取最多 4 KiB 包头及限定范围 PE 头来识别 ELF/AppImage、deb 候选和 PE；OLE 并不一概当成 MSI。此识别不是签名或完整包合法性证明。

`Verify` 对完整材料重新计算摘要。`Path` 仅验证摘要字符并构建位置；使用内容前调用 Verify。引用查询、删除计划和引用锁放在数据库/service 层，本包不提供跳过引用检查的删除入口。空间预检查查询目标文件系统，实际写入仍可能因配额、并发占用或设备错误失败；不回退为引用来源文件。

## 迁移容器：internal/migration

`Export(ctx, destination, Snapshot, []Entry, Limits)` 写 tar+gzip。destination 必须为绝对 `.zahbundle` 路径，已有目标一律拒绝。使用同目录随机 `.partial`，完整校验后无覆盖提交。每个 Entry 是明确选择的普通文件和包内名字，不自动遍历 HOME。调用者必须先停止写入者、持有环境及资源引用锁，并收集所有应包含文件；导出器检查选定文件在复制期间的大小/修改时间变化，但这不能替代冷备份锁。

Snapshot 含 Manifest、语义 Catalog JSON 和待重绑定 Bindings JSON。必须明确列出 Resources；状态为 packaged、target_required、rebind、excluded。packaged 的 Path 必须存在；有遗漏/准备/重绑定要求时禁止 OfflineReady。归档不会计算真实后端依赖是否齐备，服务层必须保守填写 OfflineReady。

格式 1 的根成员为 manifest.json、catalog.json、bindings.json、checksums.json。数据仅允许 objects/sha256、environments、data 下的普通文件。checksums.json 包含其自身以外全部成员的长度和 SHA256。objects/sha256 下文件名必须匹配内容摘要。manifest 必须首个出现。所有归档未加密，可能含个人数据；SHA256 不能证明来源可信。

`Inspect` 对全包流式校验，不解压到磁盘。`Import` 解压到目标同级私有暂存目录，验证完毕后发布为新的 target；拒绝已有 target，不覆盖现有数据库或数据。数据库登记须在 Import 成功后执行，登记失败时由调用者按本次资源清单回收新目录。

防护包括：绝对路径/穿越/反斜线/重复名、软硬链接、设备、目录条目、特权 mode、非零 UID/GID、xattrs、未知 PAX 字段；文件数、单文件、元数据和总展开字节上限；摘要集合精确匹配；截断 gzip、尾随内容和多 gzip 流。默认上限为总 64 GiB、单文件 32 GiB、元数据 8 MiB、100000 文件，可由 Limits 调整。目标架构默认 runtime.GOARCH，跨架构拒绝。

普通文件仅恢复原属主 rwx 位（0700 掩码），目录统一 0700，不重放宿主用户 ID、组权限和特权元数据。空目录、链接语义、ACL/xattr 不由格式 1 文件条目恢复：调用者须明确列为待绑定/遗漏，不能隐瞒。本 migration 包只把 Linux 镜像和 Bottles 备份作为普通文件搬运；后续 backend 恢复适配在另一个显式任务中处理。Linux 镜像交给 Docker；Bottle 文件仅展开到新建的受管暂存前缀，不能展开到宿主根目录。

## 已执行的验证

`go test -race ./internal/catalog ./internal/migration`：独立副本、中文空格名称、同内容并发去重、同名不同内容、损坏材料、大小限制、取消清理；归档往返、删除源后恢复、执行位恢复、目标防覆盖、资源遗漏、错误对象摘要、架构和展开限制、失败无目标/暂存残留、路径穿越、软硬链接、设备、特权元数据、重复条目、截断和尾随 gzip、坏摘要表。

没有执行真实后端快照、真实应用数据迁移、跨用户名电脑恢复、断电试验、加密或磁盘配额故障注入。这些不能由上述包测试推导为已通过。


## 服务层自动恢复

完整环境包先执行 `environment.restore`：校验、稳定身份合并、在所选目录创建私有子目录、恢复文件、SQLite 原子登记。已有同身份/平台/架构/版本跳过，不覆盖数据。同名不同身份保留为不同应用。

界面从 `id_map` 取得新增实例，随后逐个调用 `plan.create {operation:"environment.rebuild", input:{id}}` 和幂等 `job.submit`。此操作持有全局修改锁与环境锁；只接受 `needs_runtime_restore` 且没有启动租约的实例。`PendingEnvironmentRestore` 保存快照摘要、恢复 nonce、Bottle profile 与阶段身份。文件恢复和运行环境恢复是两个持久步骤，后一步失败不会撤销导入的用户数据。

Linux：只读检查并加载 tagless 镜像 → 新 Distrobox（独立 HOME、实例和恢复标签）→ 保存完整容器 ID → 校验镜像/UID/HOME/挂载/入口 → 精确停止。重试必须符合同一恢复标签，不能收编同名外部容器。

Bottle：验证新包 `restore.json` → 确认相同用户名、已安装 runner 和兼容配置 → 公开 CLI 新建目标 → 精确等待目标 Wine 退出 → 安全展开快照并复用新目标配置 → 非覆盖发布 → 公开 CLI 登记入口 → 验证文件摘要和精确 Wine 控制。发布前后以设备/inode 检查，失败保留阶段证据；无法可靠证明身份时停止，不借前缀名字杀进程。

两者最后通过 `ReplaceInstallation` 在一个 SQLite 事务中把实例设为 ready、环境设为 stopped，并保存入口和新资源证据。只有这一步完成，界面才显示运行。归档摘要仅验证完整性，不能证明来源可信；恢复可信归档仍会执行 Distrobox/Bottles 初始化，UI 导入确认明确说明权限与容器依赖下载。可移植范围、基线占用、真实未测项见 RELEASE_READINESS。
