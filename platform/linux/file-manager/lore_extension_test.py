# SPDX-License-Identifier: MIT
"""Tests of the extension's status cache reading, which need no file manager:
python3 platform/linux/file-manager/lore_extension_test.py"""

import os
import struct
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import lore_extension as extension  # noqa: E402


def cache(entries, magic=extension.MAGIC):
    data = bytearray(extension.HEADER_SIZE + extension.CAPACITY * extension.ENTRY.size)
    struct.pack_into("<iIQ", data, 0, len(entries), magic, 1)
    for index, (hash_, state) in enumerate(entries):
        extension.ENTRY.pack_into(data, extension.HEADER_SIZE + index * extension.ENTRY.size, hash_, state, 0, 7)
    return bytes(data)


class PathHashTest(unittest.TestCase):
    def test_matches_the_companion(self):
        # FileStatusCacheTests.HashMatchesTheExtension checks the same value in C#.
        self.assertEqual(0x95CE8F54D9FE47A7, extension.path_hash("/home/user/wc/Docs/Ёлка.txt"))

    def test_ignores_a_trailing_separator_and_keeps_case(self):
        self.assertEqual(extension.path_hash("/wc/Docs"), extension.path_hash("/wc/Docs/"))
        self.assertNotEqual(extension.path_hash("/wc/Docs"), extension.path_hash("/wc/docs"))


class ReadStatesTest(unittest.TestCase):
    def test_reads_the_states_the_companion_wrote(self):
        states = extension.read_states(cache([(11, 1), (0, 0), (13, 6)]))
        self.assertEqual({11: 1, 13: 6}, states)

    def test_reads_nothing_from_another_version_or_a_damaged_file(self):
        self.assertEqual({}, extension.read_states(cache([(11, 1)], magic=0)))
        self.assertEqual({}, extension.read_states(b"\x01\x00\x00\x00"))

    def test_ignores_unknown_states_and_a_count_beyond_the_capacity(self):
        data = bytearray(cache([(11, 99), (12, 4)]))
        struct.pack_into("<i", data, 0, extension.CAPACITY + 5)
        self.assertEqual({12: 4}, extension.read_states(bytes(data)))


if __name__ == "__main__":
    unittest.main()
