#!/usr/bin/env python3
"""Generate wordlike_generated.go from Unicode Character Database files.

Alyze keeps a UAX #29 segment as a token when any character in it is
word-like: Word_Break ALetter, Hebrew_Letter, or Numeric; Extended_Pictographic;
Ideographic; General_Category Other_Number; or a Script other than Common,
Inherited, or Unknown. Alyze 0.1.5 uses Unicode 17.0 property data.

Usage:
    python internal/generate_wordlike.py path/to/ucd-17.0.0

The directory must contain WordBreakProperty.txt, emoji-data.txt,
PropList.txt, Scripts.txt, and DerivedGeneralCategory.txt.
"""

from __future__ import annotations

import pathlib
import re
import sys


UNICODE_VERSION = "17.0.0"
LINE = re.compile(r"^([0-9A-F]+)(?:\.\.([0-9A-F]+))?\s*;\s*([A-Za-z_]+)")


def ranges(path: pathlib.Path, keep) -> list[tuple[int, int]]:
    text = path.read_text()
    if f"-{UNICODE_VERSION}.txt" not in text.splitlines()[0] and path.name != "emoji-data.txt":
        raise SystemExit(f"{path} is not Unicode {UNICODE_VERSION}")
    result = []
    for line in text.splitlines():
        match = LINE.match(line)
        if match and keep(match.group(3)):
            start = int(match.group(1), 16)
            result.append((start, int(match.group(2) or match.group(1), 16)))
    return result


def main() -> None:
    if len(sys.argv) != 2:
        raise SystemExit(f"usage: {sys.argv[0]} path/to/ucd-{UNICODE_VERSION}")
    ucd = pathlib.Path(sys.argv[1])

    collected = (
        ranges(ucd / "WordBreakProperty.txt", lambda v: v in ("ALetter", "Hebrew_Letter", "Numeric"))
        + ranges(ucd / "emoji-data.txt", lambda v: v == "Extended_Pictographic")
        + ranges(ucd / "PropList.txt", lambda v: v == "Ideographic")
        + ranges(ucd / "DerivedGeneralCategory.txt", lambda v: v == "No")
        + ranges(ucd / "Scripts.txt", lambda v: v not in ("Common", "Inherited", "Unknown"))
    )
    collected.sort()
    merged: list[list[int]] = []
    for start, end in collected:
        if merged and start <= merged[-1][1] + 1:
            merged[-1][1] = max(merged[-1][1], end)
        else:
            merged.append([start, end])

    output = [
        f"// Code generated from Unicode {UNICODE_VERSION} property data by",
        "// internal/generate_wordlike.py; DO NOT EDIT.",
        "",
        "package sid",
        "",
        "// wordLikeRanges are sorted, merged, inclusive code-point ranges.",
        "var wordLikeRanges = [...][2]rune{",
    ]
    output += [f"\t{{0x{start:04X}, 0x{end:04X}}}," for start, end in merged]
    output += ["}", ""]
    pathlib.Path("wordlike_generated.go").write_text("\n".join(output))


if __name__ == "__main__":
    main()
