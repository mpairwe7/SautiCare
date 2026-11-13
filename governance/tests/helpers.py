"""Shared fixtures: build a minimal repository tree in a temp directory."""
import importlib.util
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def load(rel: str):
    """Import a gate script by path (threat-model/ is not a package name)."""
    path = ROOT / rel
    spec = importlib.util.spec_from_file_location(path.stem, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class Tree:
    def __init__(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)

    def write(self, rel: str, text: str) -> "Tree":
        p = self.root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)
        return self

    def close(self):
        self._tmp.cleanup()


GO_TEST = "package x\n\nimport \"testing\"\n\nfunc TestReal(t *testing.T) {}\n"
FLAGS_OFF = "android_app: false\ncaller_link: false\nspeech_assist: false\napp_api: false\n"
