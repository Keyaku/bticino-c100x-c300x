#!/bin/sh
# Fails if any BTicino firmware blob or secret is tracked by git.
# This is the standing release gate: the repo ships hashes and tools only,
# never firmware images (.fwz / rootfs / kernel) or credentials.
set -eu
bad=$(git ls-files | grep -iE '\.fwz$|\.(pem|key|crt)$|(^|/)certs/|bticinokey|my_login_details|info_wifi' || true)
if [ -n "$bad" ]; then echo "TRACKED SECRETS/BLOBS:"; echo "$bad"; exit 1; fi
echo "hygiene: clean"
