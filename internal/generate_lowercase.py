#!/usr/bin/env python3
"""Generate lowercase_generated.go from Unicode Character Database files.

Alyze lowercases character by character with a table vendored from Rust's
char::to_lowercase for Unicode 17.0: the simple lowercase mapping, replaced by
the unconditional SpecialCasing.txt mapping where one exists (U+0130 becomes
"i̇"). Context-dependent mappings such as Final_Sigma are not applied.
Go's strings.ToLower uses older Unicode data and simple mappings only.

Usage:
    python internal/generate_lowercase.py path/to/ucd-17.0.0

The directory must contain UnicodeData.txt and SpecialCasing.txt.
"""

from __future__ import annotations

import pathlib
import sys


UNICODE_VERSION = "17.0.0"


def main() -> None:
    if len(sys.argv) != 2:
        raise SystemExit(f"usage: {sys.argv[0]} path/to/ucd-{UNICODE_VERSION}")
    ucd = pathlib.Path(sys.argv[1])

    special = (ucd / "SpecialCasing.txt").read_text()
    if f"SpecialCasing-{UNICODE_VERSION}.txt" not in special.splitlines()[0]:
        raise SystemExit(f"SpecialCasing.txt is not Unicode {UNICODE_VERSION}")

    mappings: dict[int, list[int]] = {}
    for line in (ucd / "UnicodeData.txt").read_text().splitlines():
        fields = line.split(";")
        if fields[13]:
            mappings[int(fields[0], 16)] = [int(fields[13], 16)]
    for line in special.splitlines():
        line = line.split("#", 1)[0].strip()
        if not line:
            continue
        fields = [field.strip() for field in line.split(";")]
        if fields[4]:
            continue  # conditional mapping
        code_point = int(fields[0], 16)
        lower = [int(value, 16) for value in fields[1].split()]
        if lower != [code_point]:
            mappings[code_point] = lower

    output = [
        f"// Code generated from Unicode {UNICODE_VERSION} casing data by",
        "// internal/generate_lowercase.py; DO NOT EDIT.",
        "",
        "package sid",
        "",
        "// lowercaseMappings are sorted by code point.",
        "var lowercaseMappings = [...]struct {",
        "\tfrom rune",
        "\tto   string",
        "}{",
    ]
    for code_point in sorted(mappings):
        lower = "".join(f"\\U{value:08X}" for value in mappings[code_point])
        output.append(f'\t{{0x{code_point:04X}, "{lower}"}},')
    output += ["}", ""]
    pathlib.Path("lowercase_generated.go").write_text("\n".join(output))


if __name__ == "__main__":
    main()
