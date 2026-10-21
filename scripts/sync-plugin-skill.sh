#!/usr/bin/env bash
# ABOUTME: Copies the canonical use-mailboxorg-cli skill into the Claude Code plugin.
# ABOUTME: Plugins install real files only, so the plugin keeps a copy that this script refreshes.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_dir="$root/.agents/skills/use-mailboxorg-cli"
target_dir="$root/plugins/mbx/skills/use-mailboxorg-cli"

mkdir -p "$(dirname "$target_dir")"
rm -rf "$target_dir"
cp -R "$source_dir" "$target_dir"
echo "Copied $source_dir to $target_dir"
