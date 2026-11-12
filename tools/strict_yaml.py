"""Safe YAML loading that rejects duplicate mapping keys.

PyYAML's SafeLoader keeps the last of two equal keys, so a repeated `risks:`
or `verified_by:` would silently discard the records the gates are meant to
check (CodeRabbit PR #5). Every governance gate loads its YAML through
`load()`.
"""
from pathlib import Path

import yaml
from yaml.constructor import ConstructorError


class _UniqueKeyLoader(yaml.SafeLoader):
    def construct_mapping(self, node, deep=False):
        seen = set()
        for key_node, _ in node.value:
            if key_node.tag == "tag:yaml.org,2002:merge":
                continue  # `<<:` merges a mapping; its keys may be overridden
            key = self.construct_object(key_node, deep=True)
            try:
                hash(key)
            except TypeError:
                continue  # the base constructor reports an unhashable key
            if key in seen:
                raise ConstructorError("while constructing a mapping", node.start_mark,
                                       f"found duplicate key {key!r}", key_node.start_mark)
            seen.add(key)
        return super().construct_mapping(node, deep)


YAMLError = yaml.YAMLError


def load(path: Path):
    """Parse a YAML file with the safe loader, failing on duplicate keys.
    Error marks name the file."""
    with open(path, encoding="utf-8") as f:
        return yaml.load(f, Loader=_UniqueKeyLoader)  # noqa: S506 - a SafeLoader subclass


def describe(err: yaml.YAMLError) -> str:
    """One-line description of a YAML error, for gate output."""
    return " ".join(str(err).split())
