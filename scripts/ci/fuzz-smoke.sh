#!/usr/bin/env bash
# Short fuzzing of every parser that takes untrusted input (Build Prompt §16).
set -euo pipefail
cd "$(dirname "$0")/../.."
T=${FUZZTIME:-20s}
go test -run='^$' -fuzz='^FuzzParseReference$' -fuzztime="$T" ./internal/incident/
go test -run='^$' -fuzz='^FuzzATForm$' -fuzztime="$T" ./internal/platform/africastalking/
go test -run='^$' -fuzz='^FuzzClassifyNeverPanics$' -fuzztime="$T" ./internal/triage/
go test -run='^$' -fuzz='^FuzzAnswerNeverPanics$' -fuzztime="$T" ./internal/intake/
go test -run='^$' -fuzz='^FuzzEventDecode$' -fuzztime="$T" ./internal/platform/ari/
