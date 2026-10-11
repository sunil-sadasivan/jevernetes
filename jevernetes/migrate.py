"""Non-destructive, idempotent import of version-1 human review rules.

Run: python3 -m jevernetes.migrate SOURCE DESTINATION
The destination remains directly usable by ReviewStore; no suppression-rule
translation or acknowledgement identity rewrite is performed.
"""
import argparse
import json
import os
from pathlib import Path
import stat
import tempfile

MAX_BYTES = 16 * 1024 * 1024


def _unique(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError('Duplicate JSON key in review rules')
        value[key] = item
    return value


def _read(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_BYTES:
            raise ValueError('Review rules must be a bounded regular file')
        raw = stream.read(MAX_BYTES + 1)
    if len(raw) > MAX_BYTES:
        raise ValueError('Review rules exceed import limit')
    try:
        value = json.loads(raw, object_pairs_hook=_unique,
                           parse_constant=lambda _: (_ for _ in ()).throw(ValueError('Invalid number')))
    except (ValueError, UnicodeError):
        raise ValueError('Malformed review rules') from None
    validate(value)
    return value


def validate(value):
    if not isinstance(value, dict) or type(value.get('version')) is not int or value['version'] != 1:
        raise ValueError('Expected version-1 human review rules')
    if not isinstance(value.get('rules'), list) or not isinstance(value.get('acknowledged'), dict):
        raise ValueError('Invalid review rules structure')
    identities = {}
    for rule in value['rules']:
        if not isinstance(rule, dict):
            raise ValueError('Invalid rule')
        for name, maximum in [('id', 256), ('pattern', 500)]:
            text = rule.get(name)
            if not isinstance(text, str) or not 1 <= len(text) <= maximum or any(ord(c) < 32 for c in text):
                raise ValueError('Invalid rule identity or literal pattern')
        if type(rule.get('enabled')) is not bool or rule.get('match', 'contains') not in ('exact', 'contains'):
            raise ValueError('Invalid rule state or match mode')
        scope = rule.get('scope')
        if not isinstance(scope, dict) or not scope or len(scope) > 32:
            raise ValueError('Invalid rule scope')
        for key, item in scope.items():
            if not isinstance(key, str) or len(key) > 256 or type(item) not in (str, int, bool) or len(str(item)) > 4096:
                raise ValueError('Invalid literal source scope')
        prior = identities.setdefault(rule['id'], rule)
        if prior != rule:
            raise ValueError('Conflicting duplicate rule identity')
    for key, ids in value['acknowledged'].items():
        if not isinstance(key, str) or len(key) > 4096 or not isinstance(ids, list):
            raise ValueError('Invalid acknowledgement scope')
        if any(not isinstance(i, str) or not 1 <= len(i) <= 256 for i in ids):
            raise ValueError('Invalid acknowledgement identity')


def import_reviews(source, destination):
    """Copy validated semantics without replacing any existing destination.

    Hard-link publication is atomic and fails on races. A crash before publication
    leaves only a private temporary file; a crash after publication leaves the
    entire valid destination. Reruns compare all fields, including disabled rules,
    duplicates, original acknowledgement keys and any extension metadata.
    """
    value = _read(source)
    destination = Path(destination)
    destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    info = destination.parent.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise ValueError('Destination directory must be owned by you with mode 0700')
    try:
        existing = _read(destination)
    except FileNotFoundError:
        existing = None
    if existing is not None:
        if existing != value:
            raise ValueError('Destination differs; nothing overwritten. Choose a new destination')
        return False
    raw = (json.dumps(value, ensure_ascii=False, indent=2) + '\n').encode()
    fd, temporary = tempfile.mkstemp(prefix='.review-import-', dir=destination.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        try:
            os.link(temporary, destination, follow_symlinks=False)
        except FileExistsError:
            if _read(destination) != value:
                raise ValueError('Destination changed; nothing overwritten') from None
            return False
        directory = os.open(destination.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        os.unlink(temporary)
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('source', type=Path)
    parser.add_argument('destination', type=Path)
    args = parser.parse_args()
    try:
        changed = import_reviews(args.source, args.destination)
    except (OSError, ValueError):
        parser.exit(1, 'Review import failed; validate version-1 input, private directory and non-conflicting destination. Nothing overwritten.\n')
    print('Imported human reviews' if changed else 'Already imported; unchanged')


if __name__ == '__main__':
    main()
