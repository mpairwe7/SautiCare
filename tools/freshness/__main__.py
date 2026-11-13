import argparse
import sys
from pathlib import Path

from tools.freshness import check

ap = argparse.ArgumentParser(prog="python3 -m tools.freshness")
ap.add_argument("--check", action="store_true", help="fail on stale documentation")
ap.add_argument("--root", default=".", help="repository root")
args = ap.parse_args()
errors = check(Path(args.root))
for e in errors:
    print("STALE:", e)
print(f"freshness: {len(errors)} problem(s)")
sys.exit(1 if errors and args.check else 0)
