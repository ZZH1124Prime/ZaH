# 开发源码依赖与许可证准备清单

更新：2026-11-01。此清单及 npm-source.cdx.json 仅来自现有锁文件/已构建开发二进制/本地依赖。最初清单生成时未安装、下载依赖或制作发行包；后续新增的 Focal 私有运行时下载/编译见下方说明，仍未制作发行包。不能当作最终安装包完整 SBOM 或法律审查结论。

## Node 直接依赖

| 范围 | 组件 | 固定版本 | 锁文件声明许可 |
| --- | --- | --- | --- |
| 运行依赖 | @tanstack/react-query | 5.105.0 | MIT |
| 运行依赖 | lucide-react | 1.66.0 | ISC |
| 运行依赖 | react | 19.3.1 | MIT |
| 运行依赖 | react-dom | 19.3.1 | MIT |
| 运行依赖 | react-router-dom | 7.18.5 | MIT |
| 开发依赖 | @tailwindcss/vite | 4.3.3 | MIT |
| 开发依赖 | @testing-library/jest-dom | 7.0.1 | MIT |
| 开发依赖 | @testing-library/react | 16.3.3 | MIT |
| 开发依赖 | @testing-library/user-event | 14.6.8 | MIT |
| 开发依赖 | @types/node | 22.20.5 | MIT |
| 开发依赖 | @types/react | 19.3.0 | MIT |
| 开发依赖 | @types/react-dom | 19.3.0 | MIT |
| 开发依赖 | @vitejs/plugin-react | 6.1.2 | MIT |
| 开发依赖 | electron | 45.2.1 | MIT |
| 开发依赖 | jsdom | 30.1.3 | MIT |
| 开发依赖 | tailwindcss | 4.3.3 | MIT |
| 开发依赖 | typescript | 7.0.2 | Apache-2.0 |
| 开发依赖 | vite | 8.3.5 | MIT |
| 开发依赖 | vitest | 5.0.5 | MIT |

锁文件共 210 个包路径，均有 license 元数据。元数据不能替代各包完整许可和 NOTICE。Electron 虽放在 devDependencies，却是发行运行时，不能在打包许可清单中排除。

## 当前 Go 开发二进制实际链接模块

`go version -m bin/zah` 的依赖清单比整个模块图更接近此二进制，但不包括 Electron/系统依赖。模块许可文件仅记录已实际找到的顶层文件；上游内嵌代码仍需打包阶段补完整声明。

| 模块 | 版本 | 已发现许可文件 |
| --- | --- | --- |
| filippo.io/age | v1.3.2 | LICENSE |
| filippo.io/hpke | v0.4.0 | LICENSE |
| github.com/dustin/go-humanize | v1.1.0 | LICENSE |
| github.com/google/uuid | v1.6.0 | LICENSE |
| github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | LICENSE |
| golang.org/x/crypto | v0.55.0 | LICENSE |
| golang.org/x/sys | v0.49.0 | LICENSE |
| modernc.org/libc | v1.79.0 | LICENSE-3RD-PARTY.md, LICENSE |
| modernc.org/mathutil | v1.7.1 | LICENSE |
| modernc.org/memory | v1.12.1 | LICENSE-LOGO, LICENSE-GO, LICENSE, LICENSE-MMAP-GO |
| modernc.org/sqlite | v1.61.1 | LICENSE-SQLITE_VEC, LICENSE-SQLITE, LICENSE-3RD-PARTY.md, LICENSE |

## 现存通知文件与下一步

- `docs/licenses/LUCIDE.txt`：已保留。
- `node_modules/electron/dist/LICENSE`、`LICENSES.chromium.html`：开发运行时自带；打包时须确定实际随包的通知文件和其他内嵌第三方声明。
- Go 工具链自身 LICENSE：最终静态 Go 分发许可清单也须纳入。
- 仓库未选定项目自身 LICENSE；由所有者决定，不替选开源/专有条款。

可在当前开发授权内继续做：锁文件/本地源码许可文件整理、校验摘要、生成源码依赖 SBOM、列出遗漏。无需运行容器或安装系统依赖。不能在尚未确定打包内容时宣称已完成分发 SBOM；最终包应依据真正随包的 Go 二进制、Electron/Chromium、JS和资源再生成并核验。

`npm-source.cdx.json` 由已有 npm 离线读取 package-lock 生成，包含开发依赖，并不识别 Electron 下载二进制内部全部 Chromium 组件。`go-development-build.json` 是可复查的模块/许可摘要清单，不冒称标准 CycloneDX/SPDX。

生成依据：[npm sbom 官方文档](https://docs.npmjs.com/cli/v10/commands/npm-sbom/)。

## 后续 Focal 私有运行时（2026-11-01）

实际下载并构建了 GLib 2.88.4、OSTree 2026.5、Flatpak 1.18.4、Bubblewrap 0.12.0、xdg-dbus-proxy 0.1.9，构建工具为 Meson 1.6.1。精确源码 URL、版本/提交和摘要在 `scripts/focal-runtime.lock.json`。源归档及各自许可文件保留在隔离构建目录，见 [FOCAL_RUNTIME](../compatibility/FOCAL_RUNTIME.md)。这些库未写入宿主系统路径，也未组装为 ZaH 发行包。后续分发必须收集实际库和其内嵌第三方许可/源码提供要求，现有 Node/Go 清单不覆盖新增原生组件；尚未声称分发许可工作完成。


### Bundled Windows fallback fonts (2026-11-03)

`internal/backend/fonts/NotoSansCJK-Regular.ttc` is unmodified Noto CJK under OFL-1.1; `DejaVuSans.ttf` is unmodified DejaVu Sans under the Bitstream Vera font license with DejaVu changes in the public domain. Exact source/size/SHA256 and original notices are in that directory. The DejaVu distro notice separately describes GPL-licensed Debian packaging; that section is not the font's license. Both font notices are copied into each newly prepared Bottle beside the fonts. No Microsoft proprietary fonts are bundled.
