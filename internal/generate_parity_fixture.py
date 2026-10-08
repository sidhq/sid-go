#!/usr/bin/env python3
"""Generate tests/testdata/alyze_parity.jsonl from the Python SDK.

Each line is a snippet call and the range sid-sdk's Rust core (Alyze)
returns for it. The cases target what the Go port reimplements: UAX #29
boundaries and word-like filtering, Unicode 17 lowercasing, and stopword
removal across mixed scripts, emoji, and format characters.

Usage:
    uv run --no-project -p 3.13 --with sid-sdk==0.2.1 \
        python internal/generate_parity_fixture.py lowercase_generated.go
"""

from __future__ import annotations

import json
import pathlib
import random
import re
import sys

from sid._snippet import bm25_snippet_with_stride

SEED = 20261008
OUTPUT = pathlib.Path("tests/testdata/alyze_parity.jsonl")

PIECES = [
    "hello", "World", "THE", "and", "U.S.A.", "e-mail", "don't", "O'Neil", "foo_bar", "3.14", "1,000",
    "v2.0", "C++", "#tag", "@user", "x=y", "a.b", "...", "—", "'", '"', "(", ")", "/", "&", "$5", "€20",
    "®", "™", "©", "👍", "👍🏽", "👨‍👩‍👧", "🇺🇸", "🏳️‍🌈", "❤️", "★", "∑", "½", "²", "①", "Ⅻ",
    "日本語", "テキスト", "ラーメン", "ー", "ひらがな", "中文字", "한국어", "ไทยภาษา", "العربية", "שָׁלוֹם",
    "ג׳ירפה", 'צה"ל', "हिन्दी", "ক্ষ", "தமிழ்", "Ελληνικά", "ΟΔΟΣ", "ς", "Σ", "Русский", "ПРИВЕТ",
    "İstanbul", "ıi", "ß", "ẞ", "Straße", "ǅ", "ﬁ", "Å", "Ω", "K", "é", "́", "a‍b",
    "a‌b", "­", "x️", "⁠", "​", "﻿", "؀", " ", "\n", "\t", " ",
    "　", "\U00010D50\U00010D51", "\U00010D70", "\U000113A0", "ꟋꟌ", "Ƛ", "\U00016D40",
]
WORDS = "the of and to in is was for on by with as at from it not que der die und le la les des il est ja ei og och в и не на".split()
LANGUAGES = (
    "danish dutch english finnish french german generic hungarian italian "
    "norwegian portuguese russian spanish swedish"
).split()
BOUNDARY_WINDOWS = (1, 2, 3, 5, 8, 13, 21, 34)


def main() -> None:
    if len(sys.argv) != 2:
        raise SystemExit(f"usage: {sys.argv[0]} path/to/lowercase_generated.go")
    rng = random.Random(SEED)

    def code_point() -> str:
        while True:
            value = rng.choice([
                rng.randrange(0x20, 0x250), rng.randrange(0x250, 0x3000), rng.randrange(0x3000, 0xD800),
                rng.randrange(0xE000, 0x10000), rng.randrange(0x10000, 0x20000), rng.randrange(0x1F000, 0x1FB00),
            ])
            if not 0xD800 <= value < 0xE000:
                return chr(value)

    def text() -> str:
        parts = []
        for _ in range(rng.randrange(1, 30)):
            roll = rng.random()
            if roll < 0.55:
                parts.append(rng.choice(PIECES))
            elif roll < 0.75:
                parts.append(rng.choice(WORDS))
            else:
                parts.append("".join(code_point() for _ in range(rng.randrange(1, 4))))
            if rng.random() < 0.6:
                parts.append(" ")
        return "".join(parts)

    cases = []
    for _ in range(400):
        content = text()
        cases += [("the", content, k, k, "english") for k in BOUNDARY_WINDOWS]
    for _ in range(3000):
        content = text()
        word = rng.choice(content.split() or [content])
        word = rng.choice([word, word.upper(), word.lower(), word.title(), word.casefold()])
        cases.append((word, content, 1, 1, rng.choice(LANGUAGES)))
    for _ in range(1500):
        content = text()
        query = " ".join(rng.choice(WORDS + content.split()) for _ in range(rng.randrange(1, 6)))
        cases.append((query, content, rng.randrange(1, 4), 1, rng.choice(LANGUAGES)))

    # Every character with a lowercase mapping, matched against its mapping.
    source = pathlib.Path(sys.argv[1]).read_text()
    for match in re.finditer(r'\{0x([0-9A-F]+), "((?:\\U[0-9A-F]{8})+)"\}', source):
        upper = chr(int(match.group(1), 16))
        lower = "".join(chr(int(value, 16)) for value in re.findall(r"\\U([0-9A-F]{8})", match.group(2)))
        cases.append((upper, f"qq {lower} rr ss", 1, 1, "generic"))
        cases.append((lower, f"qq {upper} rr ss", 1, 1, "generic"))

    OUTPUT.parent.mkdir(parents=True, exist_ok=True)
    with OUTPUT.open("w") as output:
        for query, content, window, stride, language in cases:
            start, end = bm25_snippet_with_stride(query, content, window, stride, language)
            output.write(json.dumps({
                "query": query, "content": content, "window": window,
                "stride": stride, "language": language, "want": [start, end],
            }, ensure_ascii=False) + "\n")
    print(f"wrote {len(cases)} cases to {OUTPUT}")


if __name__ == "__main__":
    main()
