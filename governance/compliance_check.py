#!/usr/bin/env python3
"""AI governance gate (ISO/IEC 42001:2023 cl. 6.1 and 7.5; NIST AI RMF 1.0
GOVERN 1.4 and MAP 4.2; CycloneDX AIBOM).

Validates governance/ai_risk_manifest.yaml:
  * every risk names a NIST AI RMF function, an ISO/IEC 42001 clause, an owner
    role, a mitigation and at least one `verified_by` pointer that resolves to
    a real test (or is an explicit `planned:<milestone>` marker);
  * every AIBOM entry has the required CycloneDX fields and data_residency UG
    (hard constraint 1);
  * a flag-controlled entry's status matches config/gated/flags.yaml, so the
    inventory can never claim a model is off while it is switched on.
Both YAML files are parsed with duplicate mapping keys rejected.
Usage: python3 governance/compliance_check.py [--root .]
"""
import argparse
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from tools import strict_yaml  # noqa: E402
from tools.freshness.pointers import is_planned, resolve  # noqa: E402

NIST_FUNCTIONS = {"GOVERN", "MAP", "MEASURE", "MANAGE"}
RISK_FIELDS = ("id", "title", "nist_function", "iso42001_clause", "owner_role", "mitigation", "verified_by")
AIBOM_FIELDS = ("bom-ref", "type", "name", "version", "supplier", "status", "data_residency")
AIBOM_TYPES = {"application", "machine-learning-model", "service"}
STATUSES = {"active", "flagged-off", "planned"}


def check(root: Path) -> list[str]:
    root = Path(root)
    errors: list[str] = []
    try:
        manifest = strict_yaml.load(root / "governance" / "ai_risk_manifest.yaml") or {}
        flags = strict_yaml.load(root / "config" / "gated" / "flags.yaml") or {}
    except strict_yaml.YAMLError as e:
        return [strict_yaml.describe(e)]

    risks = manifest.get("risks") or []
    if not risks:
        errors.append("ai_risk_manifest: no risks declared")
    seen = set()
    for r in risks:
        rid = r.get("id", "<no id>")
        if rid in seen:
            errors.append(f"{rid}: duplicate id")
        seen.add(rid)
        for f in RISK_FIELDS:
            if not r.get(f):
                errors.append(f"{rid}: missing {f}")
        if r.get("nist_function") and r["nist_function"] not in NIST_FUNCTIONS:
            errors.append(f"{rid}: nist_function {r['nist_function']!r} not in {sorted(NIST_FUNCTIONS)}")
        for ptr in r.get("verified_by") or []:
            if problem := resolve(root, ptr):
                errors.append(f"{rid}: {problem}")

    inventory = manifest.get("aibom_inventory") or []
    if not inventory:
        errors.append("ai_risk_manifest: aibom_inventory is missing or empty")
    for item in inventory:
        ref = item.get("bom-ref", "<no bom-ref>")
        for f in AIBOM_FIELDS:
            if not item.get(f):
                errors.append(f"aibom {ref}: missing {f}")
        if item.get("type") and item["type"] not in AIBOM_TYPES:
            errors.append(f"aibom {ref}: type {item['type']!r} not in {sorted(AIBOM_TYPES)}")
        if item.get("status") and item["status"] not in STATUSES:
            errors.append(f"aibom {ref}: status {item['status']!r} not in {sorted(STATUSES)}")
        if item.get("data_residency") and item["data_residency"] != "UG":
            errors.append(f"aibom {ref}: data_residency must be UG (hard constraint 1), got {item['data_residency']!r}")
        if flag := item.get("flag"):
            if flag not in flags:
                errors.append(f"aibom {ref}: flag {flag!r} not defined in config/gated/flags.yaml")
            elif bool(flags[flag]) != (item.get("status") == "active"):
                errors.append(f"aibom {ref}: status {item.get('status')!r} disagrees with {flag}={flags[flag]}")
    return errors


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default=str(ROOT))
    root = Path(ap.parse_args().root)
    errors = check(root)
    for e in errors:
        print("GOVERNANCE:", e)
    try:
        manifest = strict_yaml.load(root / "governance" / "ai_risk_manifest.yaml") or {}
    except strict_yaml.YAMLError:
        manifest = {}
    ptrs = [p for r in manifest.get("risks") or [] for p in r.get("verified_by") or []]
    planned = sum(1 for p in ptrs if is_planned(p))
    print(f"compliance_check: {len(manifest.get('risks') or [])} risks, {len(ptrs) - planned} verified pointers, "
          f"{planned} planned, {len(manifest.get('aibom_inventory') or [])} AIBOM entries, {len(errors)} problem(s)")
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
