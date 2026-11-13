"""Resolve `path::TestName` pointers to real test functions.

A pointer names a test that proves a requirement. It resolves when the name
is a test name (Go `TestX`, Python `test_x`) and the file exists and defines
that function (Go `func TestX(` or Python `def test_x(`); a helper function
is never proof.
`planned:<milestone> <text>` marks proof that a later milestone delivers; it
never resolves silently — gates count planned items separately.
"""
import re
from pathlib import Path

POINTER = re.compile(r"^(?P<path>[\w./-]+)::(?P<name>\w+)$")
TEST_NAME = re.compile(r"^(Test|test_)\w*$")


def is_planned(ptr: str) -> bool:
    return ptr.startswith("planned:")


def resolve(root: Path, ptr: str) -> str | None:
    """Return None if ptr resolves, else a problem description."""
    if is_planned(ptr):
        return None if re.match(r"^planned:(M[0-7]|P0|P2)\b", ptr) else f"malformed planned marker {ptr!r}"
    m = POINTER.match(ptr)
    if not m:
        return f"malformed test pointer {ptr!r} (want path::TestName)"
    if not TEST_NAME.match(m["name"]):
        return f"test pointer {ptr!r}: {m['name']} is not a test function name (want TestX or test_x)"
    f = root / m["path"]
    if not f.is_file():
        return f"test pointer {ptr!r}: file {m['path']} does not exist"
    name = re.escape(m["name"])
    pat = re.compile(rf"^func {name}\(|^\s*def {name}\(", re.M)
    if not pat.search(f.read_text(encoding="utf-8")):
        return f"test pointer {ptr!r}: {m['name']} not defined in {m['path']}"
    return None
