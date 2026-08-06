## mc (this client) v/s Midnight Commander (mc)

The `mc` command name is inherited from the upstream MinIO Client and conflicts
with GNU Midnight Commander on many Unix distributions. The upstream project
[declined to rename](https://github.com/minio/mc/issues?q=is%3Aissue+midnight+commander+is%3Aclosed)
its binary, and this fork keeps the upstream command name for script
compatibility. The two programs share no code or ideas — only the abbreviation
matches. Midnight Commander (mc) is a free software clone of Norton
Commander (nc), while this client is a single, fully self-contained static
binary for object storage.

To avoid the conflict, the standalone archives and Linux packages of this fork
install the binary as `mcli` — the approach
[suggested](https://github.com/minio/mc/issues/873#issuecomment-267583013) in
the upstream issue tracker. The configuration directory follows the invoked
name: `mc` uses `~/.mc`, `mcli` uses `~/.mcli`. Package managers remain free to
choose a different name:

```
mv ./mc ./mcli
```
