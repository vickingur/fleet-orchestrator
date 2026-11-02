#!/usr/bin/env python3
"""Scan a snapshot or Git index; print locations and categories, never values."""
import argparse
from pathlib import Path
import re
import subprocess
import sys

PATTERNS = {
    'private-key': re.compile(rb'-----BEGIN (?:OPENSSH |RSA |EC |DSA |ENCRYPTED )?PRIVATE KEY-----'),
    'credential-token': re.compile(rb'\b(?:ghp_|github_pat_|sk-ant-|sk-proj-|tskey-)[A-Za-z0-9_-]{12,}|\bAKIA[A-Z0-9]{16}\b'),
    'assigned-secret': re.compile(rb'''(?i)(?:api[_-]?key|access[_-]?token|password|client[_-]?secret)\s*[:=]\s*["'][A-Za-z0-9_+/=-]{20,}["']'''),
    'tailnet-domain': re.compile(rb'\b[\w.-]+\.ts\.net\b'),
    'tailnet-address': re.compile(rb'\b100\.(?:\d{1,3}\.){2}\d{1,3}\b'),
    'mac-personal-path': re.compile(rb'/' + rb'Users/[^/\s"\x27]+'),
}
BANNED = {'.git', '.ssh', '.aws', '.env', '.DS_Store', '.sessions', '.plans',
          '__pycache__', 'node_modules', 'private', 'dist', 'known_hosts', 'authorized_keys'}
BANNED_SUFFIXES = {'.pem', '.key', '.p12', '.pfx', '.log', '.jsonl', '.zip', '.gz', '.pyc'}


def inspect(rel: Path, data: bytes) -> list[str]:
    errors = []
    if any(part in BANNED or part.startswith('.env.') for part in rel.parts):
        errors.append(f'{rel}: excluded file or directory')
    if rel.suffix in BANNED_SUFFIXES:
        errors.append(f'{rel}: excluded file type')
    if b'\0' in data:
        errors.append(f'{rel}: binary content')
    for name, pattern in PATTERNS.items():
        if pattern.search(data):
            errors.append(f'{rel}: {name}')
    return errors


def scan(root: Path, *, tracked: bool, source_only: bool) -> tuple[list[str], int]:
    errors = []
    count = 0
    if tracked:
        # Inspect the index's blobs, not possibly different working-tree files.
        entries = subprocess.check_output(['git', 'ls-files', '--stage', '-z'], cwd=root)
        for entry in entries.split(b'\0'):
            if not entry:
                continue
            meta, name = entry.split(b'\t', 1)
            mode, oid, stage = meta.split()
            rel = Path(name.decode())
            if mode not in (b'100644', b'100755') or stage != b'0':
                errors.append(f'{rel}: non-regular or conflicted index entry')
                continue
            if rel.as_posix() in {'fleet.toml', 'FLEET.md'}:
                errors.append(f'{rel}: working deployment file must not be tracked')
            data = subprocess.check_output(['git', 'cat-file', 'blob', oid.decode()], cwd=root)
            errors.extend(inspect(rel, data))
            count += 1
        if count == 0:
            errors.append('Git index is empty; stage the source before scanning')
    else:
        for p in sorted(root.rglob('*')):
            rel = p.relative_to(root)
            if source_only and rel.parts[0] == 'dist':
                continue
            if p.is_symlink():
                errors.append(f'{rel}: symlink')
            elif p.is_file():
                errors.extend(inspect(rel, p.read_bytes()))
                count += 1
        # The source carries no inventory of its own; one at the root is a
        # fleet config's, and only examples/config is fictional.
        for name in ('fleet.toml', 'FLEET.md'):
            if (root / name).exists():
                errors.append(f'{name}: a fleet config file does not belong in the source; do not share it')
    return errors, count


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument('--tracked', action='store_true', help='Scan staged Git blobs, excluding local ignored files')
    mode.add_argument('--source-only', action='store_true', help='Ignore generated dist/ files in a non-Git snapshot')
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    errors, count = scan(root, tracked=args.tracked, source_only=args.source_only)
    if errors:
        print('\n'.join(errors), file=sys.stderr)
        return 1
    print(f'Privacy pattern check passed: {count} files; tracked={args.tracked}.')
    print('Pattern checks cannot prove absence of secrets. Review new content before sharing.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
