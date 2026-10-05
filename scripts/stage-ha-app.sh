#!/bin/sh
# Create a self-contained local app build context; source need not be committed.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
dest=${1:-"$root/dist/homekey_go"}

if [ "$#" -gt 1 ]; then
    echo 'Usage: sh scripts/stage-ha-app.sh [destination]' >&2
    exit 1
fi

if [ -e "$dest" ]; then
    echo "Destination already exists: $dest. Choose a new directory." >&2
    exit 1
fi

mkdir -p "$dest"

cp "$root/Dockerfile" "$root/.dockerignore" "$root/go.mod" "$root/go.sum" \
    "$root/LICENSE" "$root/NOTICE" "$dest/"

cp -R "$root/cmd" "$root/internal" "$dest/"

mkdir -p "$dest/ha-app"

cp "$root/ha-app/run.sh" "$dest/ha-app/run.sh"

# Local installation builds the Dockerfile instead of pulling a published image.
sed '/^image:/d' "$root/ha-app/config.yaml" > "$dest/config.yaml"

cp "$root/ha-app/DOCS.md" "$root/ha-app/README.md" \
    "$root/ha-app/CHANGELOG.md" "$root/ha-app/NFC.md" "$dest/"

cp -R "$root/ha-app/translations" "$dest/"

printf 'Local app build context: %s\n' "$dest"
