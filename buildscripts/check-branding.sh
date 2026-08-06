#!/usr/bin/env bash

# Brand policy gate for the Silo mc fork.
#
# Classification follows the Silo rebranding manual:
#   BRAND/UPSELL -> must not reappear in user-visible surfaces (checked here)
#   COMPAT       -> deliberately preserved and never checked: MINIO_*/MC_* env
#                   vars, x-minio-* headers, module and import paths, the
#                   minio-go user agent, --type minio enums, /minio/* metric
#                   paths, the minio-job scrape job name, the .part.minio
#                   transfer suffix, and all original copyright headers.
#
# Allowlisted files keep legacy-migration or dormant upstream logic on purpose.

set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
err() {
	echo "check-branding: $1" >&2
	fail=1
}

# 1. No MinIO-operated endpoints outside legacy config migration and the
#    dormant play-detection logic.
if git grep -nE 'play\.min\.io|dl\.min\.io' -- 'cmd/*.go' \
	':!cmd/config-old.go' ':!cmd/config-fix.go' ':!cmd/config-migrate.go' \
	':!cmd/config-v10.go' ':!cmd/license-register.go' ':!cmd/*_test.go'; then
	err "MinIO demo/download endpoint found outside the migration allowlist"
fi

# 2. No commercial upsell URLs anywhere.
if git grep -n 'min\.io/signup\|min\.io/subscription' -- ':!buildscripts/check-branding.sh'; then
	err "MinIO commercial upsell URL found"
fi

# 3. The CLI must identify as the Silo client.
if ! grep -q 'app.Usage = "Silo client' cmd/main.go; then
	err "cmd/main.go app.Usage no longer carries the Silo identity"
fi
if grep -q 'app.Author = "MinIO' cmd/main.go; then
	err "cmd/main.go app.Author reverted to MinIO"
fi

# 4. Example aliases stay de-branded.
if git grep -nw 'myminio' -- 'cmd/*.go' ':!cmd/*_test.go'; then
	err "'myminio' example alias reappeared"
fi

# 5. SUBNET connectivity must stay hard-disabled at compile time.
if ! grep -q 'func subnetServicesEnabled() bool { return false }' cmd/subnet-utils.go; then
	err "subnetServicesEnabled() is no longer hard-disabled"
fi

if [ "${fail}" -ne 0 ]; then
	echo "check-branding: FAILED - review docs/rebranding policy before changing brand surfaces" >&2
	exit 1
fi
echo "check-branding: OK"
