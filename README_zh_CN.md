<h1 align="center">mcli</h1>

<p align="center">
  <strong>审慎维护的 MinIO Client 社区分支</strong><br>
  为 Silo、Pigsty 与 S3 兼容对象存储提供带版本的客户端构建与兼容管理工具。
</p>

<p align="center">
  <a href="README.md">English</a> ·
  <a href="https://silo.pigsty.cc">文档</a> ·
  <a href="https://github.com/pgsty/mc/releases">版本发布</a> ·
  <a href="https://hub.docker.com/r/pgsty/mc">容器镜像</a> ·
  <a href="https://pigsty.cc/docs/repo/infra/list/#object-storage">Linux 软件包</a>
</p>

<p align="center">
  <a href="https://github.com/pgsty/mc/releases"><img alt="GitHub Release" src="https://img.shields.io/github/v/release/pgsty/mc?include_prereleases&label=release&logo=github"></a>
  <a href="https://hub.docker.com/r/pgsty/mc"><img alt="Docker Pulls" src="https://img.shields.io/docker/pulls/pgsty/mc?logo=docker"></a>
  <a href="go.mod"><img alt="Go Version" src="https://img.shields.io/github/go-mod/go-version/pgsty/mc?logo=go"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/license-AGPLv3-blue"></a>
</p>

> [!IMPORTANT]
> `pgsty/mc` 是由 [Pigsty](https://pigsty.cc) 独立维护、从开源 [MinIO Client](https://github.com/minio/mc) 延续而来的社区分支。本项目与 MinIO, Inc. 不存在隶属、背书或赞助关系；文中使用 “MinIO” 仅用于说明上游项目及兼容谱系。

## 概述

`pgsty/mc` 维护一条基于上游 MinIO Client 归档分支 [`77f82e18`](https://github.com/minio/mc/commit/77f82e18b5401a65958f1619df6ebb994634bd88) 的下游版本线，在上游仓库归档后继续提供可维护的客户端构建。

本分支的存在，是为了让 [Silo](https://github.com/pgsty/minio) 与 Pigsty 部署拥有可通过同一供应链构建、修补、测试和发布的兼容命令行客户端。它同时也是面向文件系统与 Amazon S3 兼容对象存储的通用客户端。

## 维护政策

活跃的 `master` 版本线覆盖：

- 构建、工具链与依赖项维护；
- 适用的安全修复与敏感输出加固；
- 针对可复现缺陷与 Silo 互操作问题的范围明确的修复；
- 带版本的归档包、Linux 软件包、校验和与多架构镜像；
- 发布自动化、文档与 Pigsty 集成。

改动保持克制，并在可行时提供测试。所有维护均为尽力而为，不承诺固定的响应、修复或发布时间。

### 范围之外

- 独立客户端路线图、新对象存储协议或假设性的新命令；
- 大规模重写或显著扩大下游差异的改动；
- 历史版本或多条支持分支；
- 商业支持、SLA、7×24 服务或 SUBNET 服务；
- 对所有 S3 实现或 MinIO 未来私有 API 的兼容保证。

## 兼容策略

本分支尽量保留：

- `github.com/minio/mc` module path、`~/.mc` 配置与用户熟悉的命令行为；
- S3 Signature V2/V4 访问方式，以及常用 MinIO/Silo 管理流程；
- `RELEASE.YYYY-MM-DDTHH-MM-SSZ` 标签与容器的 `mc` 入口；
- 源码构建使用 `mc`，独立归档包与 Linux 软件包使用 `mcli`；Silo 镜像同时提供两个命令名。

`mc update` 已被有意禁用。请通过 [Pigsty 软件仓库](https://pigsty.cc/docs/repo/infra/list/#object-storage) 或 [GitHub Releases](https://github.com/pgsty/mc/releases) 升级。

兼容是目标，而非保证。服务端专用管理命令可能随版本变化。请锁定版本、阅读版本说明、保留回滚路径，并在生产使用前针对目标服务完成测试。

## 发行产物

| 产物 | 位置 |
| :-- | :-- |
| 源码 | [`github.com/pgsty/mc`](https://github.com/pgsty/mc) |
| 独立归档包 | [GitHub Releases](https://github.com/pgsty/mc/releases)，提供 Linux、macOS、Windows 的 `amd64` 与 `arm64` 版本，命令名为 `mcli` |
| Linux 软件包 | 面向 `x86_64`/`aarch64` 的 RPM、DEB 与 APK，同时通过 [Pigsty 软件仓库](https://pigsty.cc/docs/repo/infra/list/#object-storage) 分发 |
| 容器镜像 | [`pgsty/mc`](https://hub.docker.com/r/pgsty/mc)，支持 `linux/amd64` 与 `linux/arm64` 多架构清单，以 `mc` 作为入口 |
| Silo 内置客户端 | [`pgsty/minio`](https://hub.docker.com/r/pgsty/minio) 以 `mcli` 提供本客户端，并保留 `mc` 兼容别名 |
| 文档 | [Silo 中文文档](https://silo.pigsty.cc) 与 [MinIO Client 命令参考](https://docs.min.io/community/minio-object-store/reference/minio-mc.html) |

## 快速开始

独立归档包与 Linux 软件包使用 `mcli` 命令：

```bash
mcli alias set local http://127.0.0.1:9000 ACCESS_KEY SECRET_KEY
mcli mb local/demo
mcli cp README.md local/demo/
mcli ls local/demo
```

容器保留用户熟悉的 `mc` 入口：

```bash
docker run --rm pgsty/mc:latest --version
docker run --rm pgsty/mc:latest ls play
```

> [!WARNING]
> 生产环境应锁定具体版本而不是使用 `latest`，验证校验和，使用 TLS 与最小权限凭据，并先用非生产数据测试破坏性命令。

从源码构建：

```bash
git clone https://github.com/pgsty/mc.git
cd mc
make build
./mc --version
```

## 常用命令

| 命令 | 用途 |
| :-- | :-- |
| `alias` | 配置凭据与服务端点 |
| `ls`、`tree`、`stat`、`du` | 检查存储桶、对象与本地文件 |
| `mb`、`rb` | 创建或删除存储桶 |
| `cp`、`get`、`put`、`mv`、`rm` | 传输与管理对象 |
| `mirror`、`diff`、`find` | 同步、比较与搜索数据 |
| `anonymous`、`share` | 管理匿名访问与临时 URL |
| `version`、`retention`、`legalhold`、`ilm` | 管理数据保护策略 |
| `replicate` | 配置存储桶复制 |
| `admin` | 运维兼容的 MinIO/Silo 服务端 |

运行 `mcli --help`、`mcli <command> --help`，或查阅[客户端命令参考](https://docs.min.io/community/minio-object-store/reference/minio-mc.html)了解完整命令集。

## 参与贡献

欢迎安全与依赖项更新、可复现缺陷修复、互操作测试、发布自动化、打包与文档改进。

Issue 与 Pull Request 应说明受影响的客户端与服务端版本、复现步骤、影响、预期行为、测试与兼容性说明。大型改动请先提交 Issue 讨论。

## 背景

上游 [`minio/mc`](https://github.com/minio/mc) 仓库在 2025 年最后一条开发线后归档。Pigsty 维护此分支，是因为 Silo 需要可复现的配套客户端发布渠道，而不能继续依赖已经归档的上游项目。

相关上游变化、替代方案评估与早期分支维护记录见以下文章：

| 文章 | 主题 |
| :-- | :-- |
| [MinIO已死](https://vonng.com/db/minio-is-dead/) | 上游项目与发行模式的变化 |
| [MinIO已死，谁能接盘？](https://vonng.com/db/minio-alternative/) | 可选替代方案评估 |
| [MinIO 已死，MinIO 复生](https://vonng.com/db/minio-resurrect/) | 建立服务端与客户端发行流水线 |
| [续命 MinIO：承诺兑现](https://vonng.com/db/minio-promise-kept/) | 初期安全与维护工作 |

## 许可证与商标

客户端继续采用 [GNU Affero General Public License v3.0](LICENSE) 发布。上游作者与署名信息见 [`CREDITS`](CREDITS)。

MinIO 是 MinIO, Inc. 的商标。Pigsty、Silo、`pgsty/mc` 与 `mcli` 均为独立社区项目，与 MinIO, Inc. 不存在隶属或背书关系。
