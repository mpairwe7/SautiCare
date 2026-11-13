#!/usr/bin/env python3
"""Threat-model gate (STRIDE for security, LINDDUN for privacy).

Validates threat-model/threats.yaml: every threat has an id, title, a STRIDE
or LINDDUN category, an asset, mitigations and `verified_by` pointers that
resolve (or are `planned:<milestone>`) and the hard constraints it protects,
and every one of Build Prompt §3's fourteen hard constraints is covered by at
least one threat. Duplicate mapping keys are rejected.
Usage: python3 threat-model/validate_threats.py [--root .]
"""
import argparse
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from tools import strict_yaml  # noqa: E402
from tools.freshness.pointers import is_planned, resolve  # noqa: E402

STRIDE = {"Spoofing", "Tampering", "Repudiation", "Information disclosure", "Denial of service",
          "Elevation of privilege"}
LINDDUN = {"Linking", "Identifying", "Non-repudiation", "Detecting", "Data disclosure", "Unawareness",
           "Non-compliance"}
HARD_CONSTRAINTS = range(1, 15)


def check(root: Path) -> list[str]:
    root = Path(root)
    try:
        data = strict_yaml.load(root / "threat-model" / "threats.yaml") or {}
    except strict_yaml.YAMLError as e:
        return [strict_yaml.describe(e)]
    errors: list[str] = []
    covered: set[int] = set()
    for t in data.get("threats") or []:
        tid = t.get("id", "<no id>")
        for f in ("id", "title", "asset", "hard_constraints", "mitigations", "verified_by"):
            if not t.get(f):
                errors.append(f"{tid}: missing {f}")
        stride, linddun = t.get("stride"), t.get("linddun")
        if not ((stride in STRIDE) or (linddun in LINDDUN)):
            errors.append(f"{tid}: category must be a STRIDE or LINDDUN value (got stride={stride!r}, linddun={linddun!r})")
        for ptr in t.get("verified_by") or []:
            if problem := resolve(root, ptr):
                errors.append(f"{tid}: {problem}")
        covered.update(int(c) for c in t.get("hard_constraints") or [])
    for hc in HARD_CONSTRAINTS:
        if hc not in covered:
            errors.append(f"hard constraint {hc} is not covered by any threat")
    return errors


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default=str(ROOT))
    root = Path(ap.parse_args().root)
    errors = check(root)
    for e in errors:
        print("THREAT-MODEL:", e)
    try:
        data = strict_yaml.load(root / "threat-model" / "threats.yaml") or {}
    except strict_yaml.YAMLError:
        data = {}
    ptrs = [p for t in data.get("threats") or [] for p in t.get("verified_by") or []]
    planned = sum(1 for p in ptrs if is_planned(p))
    print(f"validate_threats: {len(data.get('threats') or [])} threats, {len(ptrs) - planned} verified pointers, "
          f"{planned} planned, {len(errors)} problem(s)")
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
