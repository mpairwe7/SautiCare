#!/usr/bin/env bash
# Stage exactly what is uploaded to the Hugging Face Space (ADR-0039;
# docs/runbooks/demo-hf-space.md): the committed tree (git archive HEAD),
# minus every path .dockerignore excludes, plus deploy/hf-space/README.md as
# the Space card at the root. Refuses if any *.pdf, any sa/ directory or any
# file over 5 MiB was staged: the governing documents are confidential.
#
#   scripts/deploy/hf-space-stage.sh <outdir>      (outdir must be new or empty)
set -euo pipefail
[ $# -eq 1 ] || { echo "usage: $0 <outdir>" >&2; exit 2; }
out=$(realpath -m "$1")
cd "$(dirname "$0")/../.."
if [ -e "$out" ] && [ -n "$(ls -A "$out")" ]; then
  echo "hf-space-stage: $out is not empty" >&2
  exit 1
fi
if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
  echo "hf-space-stage: warning: uncommitted changes are NOT staged (HEAD only)" >&2
fi

# .dockerignore patterns as git glob pathspecs: a pattern excludes what it
# matches and, as in Docker, everything under a directory it matches; a
# "!path" line re-includes one literal path, as Docker does when it comes
# after the pattern that excluded it.
excludes=() includes=()
while IFS= read -r line || [ -n "$line" ]; do
  line=${line%$'\r'}
  case $line in ''|'#'*) continue ;; esac
  pat=${line#!}
  pat=${pat#/}
  pat=${pat%/}
  if [ "${line:0:1}" != "!" ]; then
    excludes+=(":(exclude,glob)$pat" ":(exclude,glob)$pat/**")
  elif [[ $pat == *[*?[]* ]]; then
    echo "hf-space-stage: unsupported re-include pattern $line (literal paths only)" >&2
    exit 1
  else
    includes+=("$pat")
  fi
done < .dockerignore

mkdir -p "$out"
git archive --format=tar HEAD -- . "${excludes[@]}" | tar -x -C "$out"
for inc in "${includes[@]}"; do
  if git cat-file -e "HEAD:$inc" 2>/dev/null; then
    git archive --format=tar HEAD -- ":(literal)$inc" | tar -x -C "$out"
  fi
done
git show HEAD:deploy/hf-space/README.md > "$out/README.md"

bad=$(find "$out" \( -iname '*.pdf' -o \( -type d -name sa \) -o \( -type f -size +5242880c \) \) -print)
if [ -n "$bad" ]; then
  echo "hf-space-stage: REFUSED, these must never reach the Space:" >&2
  echo "$bad" >&2
  rm -rf "$out"
  exit 1
fi
files=$(find "$out" -type f | wc -l)
size=$(du -sh "$out" | cut -f1)
echo "hf-space-stage: staged $files files ($size) from $(git rev-parse --short HEAD) in $out"
