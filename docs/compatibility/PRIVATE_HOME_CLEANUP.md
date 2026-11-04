# 私有 HOME 清理阻碍：只读诊断

实际失败操作是 `instance.data.remove` 的安全预检，目标为本次测试实例的专属 HOME。`internal/service/cleanup_tree.go:139` 的 `ensureNoCleanupWriters` 枚举同 UID 进程，检查 cwd/root/fd 是否引用待删目录；读取 `/proc/2166/cwd` 返回 Permission denied，操作未进入永久删除。

只读核对：当时 PID 2166 的名称为 `(sd-pam)`，UID/GID 1000，TracerPid 0，Seccomp 0，NoNewPrivs 0，AppArmor 为 unconfined；主机 `ptrace_scope=1`、`suid_dumpable=2`。没有读取该进程的环境、凭据或其他私密内容，没有停止它，也没有尝试修改安全设置。

这是内核 proc 访问拒绝触发应用内的保守失败，**不是工具审批拒绝**。读取 proc cwd 受 `PTRACE_MODE_READ_FSCREDS` 检查约束，见 [Linux proc_pid_cwd 手册](https://man7.org/linux/man-pages/man5/proc_pid_cwd.5.html)。同 UID 不保证可读。没有取得该进程的实际 dumpable 状态，不能仅凭 Yama=1 就断言唯一原因；不建议为清理数据放宽 ptrace、改 proc 权限或给 ZaH root 权限。

已完成且不受阻碍：身份校验后的受管 Bottle 删除，以及现有 `instance.forget` 的非破坏性解除登记。测试 HOME、恢复暂存、备份和审计保留。本轮没有再次尝试永久删除，也没有关闭安全校验。

建议下一步的最小可恢复方案（仅提案，尚未实现）：环境停止且精确移除后，把“移除应用/版本”和“永久释放数据”拆为内部两阶段；前一阶段保留原位置数据并写入可恢复 tombstone，界面明确显示保留数据大小与失败原因，恢复可重新登记。后一阶段仍独立执行严格冷清理。优先原地保留而非移动目录，以免未知进程持有路径时造成新风险。用户不必为了删除卡片调整系统安全策略。

这需确认保留期限/恢复入口与磁盘占用提示的产品语义，再实现状态与回归；不是通过 `rm -rf` 或改权限绕过已有拒绝。本轮无需额外系统权限，永久清理阻碍保持未解决状态。

## 2026-11-03：删除环境前预检（已实现）

管理页的 `instance.remove` 计划现在必须携带 `precheck_private_data:true`；Electron 拒绝省略或关闭该标志。Go 在计划创建和 worker 删除环境前复用受管 HOME 路径、所有权、文件类型、挂载和同用户进程引用检查。失败明确提示“尚未删除此应用实例的环境或数据”，不会提交或执行该实例的环境删除。随后真正执行 `instance.data.remove` 时，原有运行环境缺席证明、树指纹及进程引用复查全部保留。CLI 的独立环境删除仍保留原接口语义。

Windows 的环境删除会永久删除 Bottle 内的 C: 盘应用文件、配置和保存数据；只保留另外的受管 HOME 不等于保留全部 Windows 数据。界面确认文字与计划影响说明已区分两者。

使用已有联合迁移 QA 的 Linux 和 Windows 实例，仅执行真实 `plan.create`，未提交 `job.submit`。两次均被 `/proc/17971/cwd: permission denied` 提前拒绝；前后仍为 2 个实例、2 个环境、11 个任务、11 个计划，容器状态、HOME 与 Bottle 目录元数据一致。证据：`/home/zzh/Documents/Codex/2026-11-03/task-2/cleanup-precheck-build/real-precheck-proof.json`。

这是提前阻止已知部分删除的修复，不是永久清理已通过。预检不能阻止之后的外部写入或权限变化；实际执行仍可能失败，批量操作仍逐项报告结果。未放宽 `/proc` 检查、改宿主权限、实现回收站或执行真实数据删除。前述可恢复清理提案仍未实现。
