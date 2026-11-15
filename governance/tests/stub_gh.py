"""A stand-in for `gh` that never touches the network (tools/mergegate tests).

It reads a JSON scenario from the file named by STUB_GH_SCENARIO: a list of
rules {"argv": [tokens], "exit": int, "stdout": str, "stderr": str,
"times": int, "sleep": float}. It answers with the first rule whose tokens
each occur, in order, inside successive arguments, skipping a rule already
used "times" times, and appends {"argv", "rule"} as one JSON line to
STUB_GH_LOG. With no matching rule it exits 99, so an unexpected call fails
closed. Not a test module: unittest collects only test*.py.
"""
import json
import os
import sys
import time


def matches(tokens, args):
    i = 0
    for tok in tokens:
        while i < len(args) and tok not in args[i]:
            i += 1
        if i == len(args):
            return False
        i += 1
    return True


def main():
    args = sys.argv[1:]
    with open(os.environ["STUB_GH_SCENARIO"], encoding="utf-8") as f:
        rules = json.load(f)
    log_path = os.environ["STUB_GH_LOG"]
    used = []
    if os.path.exists(log_path):
        with open(log_path, encoding="utf-8") as f:
            used = [json.loads(line)["rule"] for line in f if line.strip()]
    for idx, rule in enumerate(rules):
        if "times" in rule and used.count(idx) >= rule["times"]:
            continue
        if matches(rule["argv"], args):
            break
    else:
        idx, rule = None, None
    with open(log_path, "a", encoding="utf-8") as f:
        f.write(json.dumps({"argv": args, "rule": idx}) + "\n")
    if rule is None:
        sys.stderr.write(f"stub gh: no rule for {args}\n")
        return 99
    time.sleep(rule.get("sleep", 0))
    sys.stdout.write(rule.get("stdout", ""))
    sys.stderr.write(rule.get("stderr", ""))
    return rule.get("exit", 0)


if __name__ == "__main__":
    raise SystemExit(main())
