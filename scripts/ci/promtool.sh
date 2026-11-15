#!/usr/bin/env bash
# Alert rules (ADR-0041 rule 8): promtool checks the rules file, then runs its
# unit tests, which show each alert firing and quiet.
set -euo pipefail
cd "$(dirname "$0")/../.."
scripts/tools/install-promtool.sh >/dev/null
bin/promtool check rules build/prometheus/sauti-rules.yml
bin/promtool test rules build/prometheus/sauti-rules.test.yml
