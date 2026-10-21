#!/usr/bin/env bash
# ABOUTME: Builds one Claude Desktop extension (.mcpb) per platform from this checkout.
# ABOUTME: Cross-compiles mbx, fills in packaging/mcpb/manifest.json and packs it with the MCPB CLI.
set -euo pipefail

usage() {
	echo "usage: scripts/build-mcpb.sh VERSION [OUTPUT_DIR]" >&2
	echo "  VERSION looks like v1.2.3; OUTPUT_DIR defaults to dist" >&2
	exit 2
}

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || usage
version="$1"
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || usage
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="${2:-dist}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
module_path="$(cd "$root" && go list -m)"
# Pinned so that a new CLI release cannot change the bundle format unnoticed.
mcpb=(npx --yes @anthropic-ai/mcpb@2.1.2)

work="$(mktemp -d "${TMPDIR:-/tmp}/mbx-mcpb.XXXXXX")"
trap 'rm -rf -- "$work"' EXIT

# Claude Desktop runs on macOS and Windows, and as a beta on Linux.
targets=(darwin/arm64 darwin/amd64 windows/amd64 windows/arm64 linux/amd64 linux/arm64)
for target in "${targets[@]}"; do
	goos="${target%/*}"
	goarch="${target#*/}"
	platform="$goos"
	binary="mbx"
	if [ "$goos" = windows ]; then
		platform="win32"
		binary="mbx.exe"
	fi
	stage="$work/$goos-$goarch"
	mkdir -p "$stage/server"
	(cd "$root" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
		go build -trimpath -ldflags="-s -w -X $module_path/internal/cli.version=$version" \
		-o "$stage/server/$binary" ./cmd/mbx)
	python3 - "$root/packaging/mcpb/manifest.json" "$stage/manifest.json" "${version#v}" "$platform" "$binary" <<'PY'
import json, sys
source, target, version, platform, binary = sys.argv[1:]
with open(source) as f:
    manifest = json.load(f)
manifest["version"] = version
manifest["server"]["entry_point"] = "server/" + binary
manifest["server"]["mcp_config"]["command"] = "${__dirname}/server/" + binary
manifest["compatibility"]["platforms"] = [platform]
with open(target, "w") as f:
    json.dump(manifest, f, indent=2)
    f.write("\n")
PY
	cp "$root/LICENSE" "$stage/LICENSE"
	"${mcpb[@]}" validate "$stage/manifest.json" >/dev/null
	"${mcpb[@]}" pack "$stage" "$out/mbx-$version-$goos-$goarch.mcpb" >/dev/null
	echo "Built $out/mbx-$version-$goos-$goarch.mcpb"
done
