#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Blair Hamilton
# SPDX-License-Identifier: Apache-2.0
#
# Decides what a change touches. Writes to $GITHUB_OUTPUT:
#   code=true|false  anything but prose changed
#   matches={...}    for each named filter, "true" if any changed file
#                    matches one of its regexes (grep -E), else "false"
#
# Env: EVENT_NAME, BASE (the commit to diff from), FILTERS (JSON object of
# name -> list of regexes; empty means {}).
#
# Anything that is not a pull request, or a pull request whose base commit
# is not present, answers "true" everywhere: an empty diff is
# indistinguishable from "only prose changed", so when unsure this is wrong
# in the direction of running the jobs.
set -euo pipefail

filters="${FILTERS:-}"
[ -n "$filters" ] || filters='{}'
if ! jq -e 'type == "object" and all(.[]; type == "array" and all(.[]; type == "string"))' \
  <<<"$filters" >/dev/null 2>&1; then
  echo "::error::filters must be a JSON object of name -> list of regexes, got: $filters"
  exit 1
fi
# A regex grep cannot compile would match nothing and read as "unchanged".
while IFS= read -r re; do
  [ -n "$re" ] || continue
  rc=0
  grep -qE -- "$re" </dev/null || rc=$?
  if [ "$rc" -gt 1 ]; then
    echo "::error::filter regex does not compile: $re"
    exit 1
  fi
done < <(jq -r '.[][]' <<<"$filters")

everything=false
files=""
if [ "${EVENT_NAME:-}" != pull_request ]; then
  echo "${EVENT_NAME:-no event} — running everything"
  everything=true
elif [ -z "${BASE:-}" ] || ! git cat-file -e "${BASE}^{commit}" 2>/dev/null; then
  echo "base commit '${BASE:-}' is not present — running everything"
  everything=true
else
  files="$(git diff --name-only "$BASE"...HEAD)"
fi

code=false
if [ "$everything" = true ]; then
  code=true
else
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    case "$f" in
      docs/*|*.md|LICENSE|LICENSE.*|NOTICE|*.png|*.jpg|*.gif|*.svg) ;; # prose
      *) code=true; break ;;
    esac
  done <<<"$files"
fi

matches='{}'
while IFS= read -r name; do
  [ -n "$name" ] || continue
  hit=false
  if [ "$everything" = true ]; then
    hit=true
  else
    while IFS= read -r re; do
      if [ -n "$files" ] && grep -qE -- "$re" <<<"$files"; then
        hit=true
        break
      fi
    done < <(jq -r --arg n "$name" '.[$n][]' <<<"$filters")
  fi
  matches="$(jq -c --arg n "$name" --arg v "$hit" '. + {($n): $v}' <<<"$matches")"
done < <(jq -r 'keys[]' <<<"$filters")

echo "code=$code" >> "$GITHUB_OUTPUT"
echo "matches=$matches" >> "$GITHUB_OUTPUT"
echo "code=$code matches=$matches"
