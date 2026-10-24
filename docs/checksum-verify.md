# Checksum verification

The SILO website maintains the command reference and design:

- Command reference: [English](https://silo.pgsty.com/reference/minio-mc/mc-checksum-verify/) · [中文](https://silo.pgsty.com/zh/reference/minio-mc/mc-checksum-verify/)
- Design and output contract: [English](https://silo.pgsty.com/blog/design/checksum-verify/) · [中文](https://silo.pgsty.com/zh/blog/design/checksum-verify/)

Implementation: [command](../cmd/checksum-verify.go), [S3 helper](../cmd/client-s3-checksum.go), and [tests](../cmd/checksum-verify_test.go).
