# age 加密封装

`.zahbundle.age` 是现有 `.zahbundle` 外面的一层标准 age 加密，不是新的归档格式。依赖锁定 `filippo.io/age v1.3.2`，采用其 X25519 recipient API；程序不自行实现密码算法，不调用 age 命令行，不隐式执行插件、SSH 命令、口令提示或密钥发现。

核对来源：[官方 Go 库](https://pkg.go.dev/filippo.io/age@v1.3.2)、[v1.3.2 发布说明](https://github.com/FiloSottile/age/releases/tag/v1.3.2)。发布说明记录了 header 大小和 recipient 数量限制的强化。本项目额外限制为 1..32 个不同 X25519 公钥，identity 文件最多 64 KiB、最多 32 个明文 X25519 identity；接受空行和 `#` 注释。

## API 与数据流

`Service.PlanEncryptBundle(ctx, EncryptBundleInput{Path, Destination, Recipients})` 校验现有 bundle、公钥、无覆盖目标，固定源文件 SHA256。`PlanDecryptBundle(ctx, DecryptBundleInput{Path, Destination, IdentityPath})` 固定密文摘要及用户选择的 identity 文件元数据。`BundleEncryptionPlan` 的 JSON 字段为 operation、path、destination、sha256、size、recipients 或 identity、warnings；operation 取 `bundle.encrypt` / `bundle.decrypt`。

identity 计划只包含 path/device/inode/size/modified_unix_nano，不读取或保存私钥正文、私钥摘要。执行时通过 `ExecuteBundleEncryption` 读取此前选择的文件；文件必须属于当前用户、仅 owner 可读写、是普通文件且路径不经过符号链接。内容只在进程内解析，错误不会引用私钥文本。输入缓冲尽力清零，但 Go/age 库中的解析对象由 Go 内存管理器管理，不承诺无内存副本、锁页或不可交换内存。

加密先把经过摘要核验的原始 bundle 流式复制到目标目录下的私有 staging，调用 `migration.Inspect` 校验，再通过 age 库写密文。解密流式读取已固定摘要的密文，完整读取至 age 认证尾部，解密内容先写入私有 staging；只有认证成功、密文摘要一致且 `migration.Inspect` 验证通过才发布明文。未经验证的明文不会发布到用户目标路径。

默认明文字节上限 64 GiB、密文字节上限 64 GiB + 64 MiB；解压后的文件数、总字节、单文件及元数据继续受现有 bundle Limits 约束。流式复制检查 context 取消。staging 目录 0700，文件 0600；文件 fsync 后通过同文件系统硬链接无覆盖发布，再 fsync 目标目录。失败清理 staging；已经发布但目录同步失败返回 `published_durability_unknown`，如实保留目标证据，不声称未发生写入。

## 操作边界

- age 提供接收方保密性及密文完整性，**不提供来源签名或发送者认证**。持有公钥的任何人都能生成可解密文件。
- 原始明文 bundle 保留；加密成功不自动删除源文件。解密发布的明文也需要用户按敏感数据处理。
- 删除 staging 不是磁盘安全擦除；文件系统、备份、交换空间或断电恢复可能保留历史块。没有伪装成提供安全擦除能力。
- 解密不导入应用、不启动容器、不自动恢复环境。后续仍使用普通 bundle 检查/恢复流程。
- 用户须自行保存私钥。未提供密钥生成/轮转/托管、密码加密、SSH identity、插件 identity、armor 格式或签名工作流。

## 验证

测试使用真实 age Go 库和临时目录，包含标准库 API 互操作与 bundle 往返、错误密钥、密文篡改/截断/尾随内容、源摘要变化、明文/密文字节及解包限制、取消、目标不覆盖、identity 替换/符号链接/不安全权限、认证成功但非 bundle 的明文拒绝，以及计划 JSON 不包含私钥。未调用外部加密程序、真实应用或网络配置操作。依赖下载沿用原有 Go 网络设置。
