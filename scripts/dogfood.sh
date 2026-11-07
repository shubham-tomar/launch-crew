#!/usr/bin/env bash
# Tag a release of this repo so the Launch Crew webhook fires on itself.
# Usage: scripts/dogfood.sh <version> [--dry-run]   (version like 0.1.0)
set -euo pipefail

usage() { echo "usage: $0 <version> [--dry-run]" >&2; exit 2; }

version="" dry=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) dry=1 ;;
    -*) usage ;;
    *) [ -z "$version" ] || usage; version="$arg" ;;
  esac
done
[ -n "$version" ] || usage
version="${version#v}"
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.]+)?$ ]]; then
  echo "version must look like 0.1.0" >&2; exit 2
fi
tag="v$version"

branch="$(git rev-parse --abbrev-ref HEAD)"
if [ "$branch" != "main" ]; then
  echo "refusing: on branch '$branch', not main" >&2; exit 1
fi
if [ -n "$(git status --porcelain)" ]; then
  echo "refusing: working tree is dirty" >&2; exit 1
fi
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  echo "refusing: tag $tag already exists" >&2; exit 1
fi

# owner/repo from origin, e.g. git@github.com:me/launchcrew.git
slug=""
if url="$(git remote get-url origin 2>/dev/null)"; then
  slug="$(printf '%s' "$url" | sed -E 's#^(git@github\.com:|https://github\.com/)##; s#\.git$##')"
fi
if [ -z "$slug" ] && [ "$dry" -eq 0 ]; then
  echo "refusing: no origin remote" >&2; exit 1
fi
slug="${slug:-<owner>/<repo>}"

if [ "$dry" -eq 1 ]; then
  echo "dry run: would run: git tag -a $tag -m 'Launch Crew $tag'"
  echo "dry run: would run: git push origin $tag"
else
  git tag -a "$tag" -m "Launch Crew $tag"
  git push origin "$tag"
fi

echo "release: https://github.com/$slug/releases/tag/$tag"
echo "captain: https://launchcrewbot.app/captain"
