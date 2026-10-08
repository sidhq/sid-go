#!/usr/bin/env python3
"""Generate stopwords_generated.go from Alyze's stopwords.rs.

Usage:
    python internal/generate_stopwords.py path/to/alyze/src/analyze/stopwords.rs
"""

from __future__ import annotations

import json
import pathlib
import re
import sys


LANGUAGES = (
    "danish",
    "dutch",
    "finnish",
    "french",
    "german",
    "hungarian",
    "italian",
    "norwegian",
    "portuguese",
    "russian",
    "spanish",
    "swedish",
)

ENGLISH = (
    "a an and are as at be but by for if in into is it no not of on or such "
    "that the their then there these they this to was will with"
).split()


def main() -> None:
    if len(sys.argv) != 2:
        raise SystemExit(f"usage: {sys.argv[0]} path/to/stopwords.rs")

    source = pathlib.Path(sys.argv[1]).read_text()
    words: dict[str, list[str]] = {"english": ENGLISH}
    for language in LANGUAGES:
        match = re.search(
            rf"pub const {language.upper()}:.*?=\s*phf::phf_set!\s*\{{(.*?)\n\}};",
            source,
            re.DOTALL,
        )
        if match is None:
            raise SystemExit(f"missing {language} stopwords")
        words[language] = [
            json.loads(f'"{value}"')
            for value in re.findall(r'"((?:\\.|[^"])*)"', match.group(1))
        ]

    output = [
        "// Code generated from Alyze 0.1.5 stopword data; DO NOT EDIT.",
        "",
        "package sid",
        "",
        "var stopwordSets = map[Language]map[string]struct{}{",
    ]
    for language in ("danish", "dutch", "english", *LANGUAGES[2:]):
        values = ", ".join(f"{json.dumps(word, ensure_ascii=False)}: {{}}" for word in words[language])
        output.append(f"\tLanguage{language.title()}: {{{values}}},")
    output.extend(("}", ""))
    pathlib.Path("stopwords_generated.go").write_text("\n".join(output))


if __name__ == "__main__":
    main()
