#!/usr/bin/env python3
"""Extract one release's section from CHANGELOG.md as a GitHub release body.

The changelog is the only place the release notes are written, so this reads them
from there rather than asking a human to retype them into the release form. Run by
`.github/workflows/release.yml` before anything is built, so a tag whose version is
missing from the changelog fails in seconds instead of after a multi-arch build.

Inputs come from the workflow environment: GITHUB_REF_NAME is the tag (`v0.1.0`),
and the section is looked up by the tag without its `v` (`## [0.1.0]`), which is the
spelling the changelog uses. Writes the body to the path given as the argument.
"""

from __future__ import annotations

import os
import re
import sys
from pathlib import Path

# A markdown link's target: the part between `](` and the closing `)`.
LINK = re.compile(r"\]\(([^)]+)\)")
# Targets that are already resolvable from anywhere.
ABSOLUTE = re.compile(r"^(?:[a-z][a-z0-9+.-]*:|//|#)", re.IGNORECASE)
# A trailing reference-link definition block, e.g. `[0.1.0]: https://…`.
REFERENCE = re.compile(r"^\[[^\]]+\]:\s*\S+$")


def section(changelog: str, version: str) -> str:
    """The body of the `## [version]` section, without its heading."""
    found = re.search(
        rf"^## \[{re.escape(version)}\][^\n]*\n(.*?)(?=^## \[|\Z)",
        changelog,
        re.DOTALL | re.MULTILINE,
    )
    if not found:
        return ""
    lines = found.group(1).strip().splitlines()
    # The last section of the file is followed by the reference-link definitions,
    # which belong to the whole changelog and not to this release.
    while lines and (REFERENCE.match(lines[-1]) or not lines[-1].strip()):
        lines.pop()
    return "\n".join(lines)


def absolutize(body: str, base: str) -> str:
    """Point repository-relative links at this tag.

    A release body is not rendered inside the repository tree, so `(LICENSE)` and
    `(documents/en/01-quickstart.md)` would 404. Pinning them to the tag rather than
    to the default branch also means the notes keep describing the release they
    belong to after main has moved on.
    """

    def repoint(match: re.Match[str]) -> str:
        target = match.group(1).strip()
        if ABSOLUTE.match(target):
            return match.group(0)
        return f"]({base}/{target.removeprefix('./')})"

    return LINK.sub(repoint, body)


def main() -> int:
    if len(sys.argv) != 2:
        print(f"usage: {sys.argv[0]} <output-path>", file=sys.stderr)
        return 2
    out = Path(sys.argv[1])

    tag = os.environ["GITHUB_REF_NAME"]
    version = tag.removeprefix("v")
    server = os.environ.get("GITHUB_SERVER_URL", "https://github.com")
    repo = os.environ["GITHUB_REPOSITORY"]

    changelog = Path("CHANGELOG.md").read_text(encoding="utf-8")
    body = section(changelog, version)
    if not body:
        print(
            f"CHANGELOG.md has no `## [{version}]` section for tag {tag}. "
            f"Add the section (or fix the tag) and push the tag again.",
            file=sys.stderr,
        )
        return 1

    out.write_text(
        absolutize(body, f"{server}/{repo}/blob/{tag}") + "\n",
        encoding="utf-8",
    )
    print(f"Wrote {out} ({len(body.splitlines())} lines) for {tag}.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
