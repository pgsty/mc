# SILO mcli Guide

`pgsty/mc` is the maintained client for `pgsty/silo`. Its administration
commands are release-tested against SILO. Operation against unmodified upstream
MinIO and other S3-compatible services is best effort, not a release guarantee.
Retained command names, `MC_*` variables, `x-minio-*` protocol fields, config
formats, and historical import paths do not by themselves create an upstream
support commitment.

Use `github.com/pgsty/silo-pkg/v3` directly and reject new maintained-source
imports of `github.com/minio/pkg/v3`. Do not lower or replace the SILO package to
make an upstream dependency graph compile. `github.com/minio/minio-go/v7` is the
explicit exception and should track the verified upstream release/commit.

Keep generic S3 and upstream-MinIO compatibility when inexpensive, but record
failures as advisory compatibility findings unless the user explicitly makes
them release-blocking. Functional and release gates should use `pgsty/silo`.

## Git 内容与大文件规则

用户于 2026-10-10 明确的通用仓库规则：

- 仓库默认保存源代码、配置和必要的轻量文档、元数据。构建产物、可执行文件、软件包、容器/虚拟机镜像、下载压缩包、数据库导出和大体积原始数据默认不进入 Git；文本格式的数据大文件同样需要检查。
- 在工作树内下载或生成上述文件之前，先选择仓库外的制品/数据位置，或补上合理的 `.gitignore` 规则并验证实际路径已被忽略，防止暂存和代理恢复/差异快照将它们写入 Git 对象库。
- 暂存、提交前检查新增文件的大小、类型和 staged 文件列表，只加入本次需要的源文件；不得用强制添加绕过忽略规则。优先保留来源清单、校验值及可复现的下载/构建方法。
- 对二进制素材、数据集或生成文件是否应纳入版本控制拿不准时，先问用户，再暂存或提交；确需例外时也先取得用户明确指示。
- 本规则不授权删除已有文件、stash、恢复快照或改写 Git 历史。忽略规则不会清除已存在的 Git 对象，历史清理需要单独明确范围。
