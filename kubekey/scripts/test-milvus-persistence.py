#!/usr/bin/env python3
"""Persistence readback must consume the original collection without writes."""
import copy
import pathlib
import runpy
import sys
import types
import unittest
from unittest import mock

dependencies = {"boto3": types.SimpleNamespace(), "botocore": types.SimpleNamespace(),
                "botocore.config": types.SimpleNamespace(Config=mock.Mock()),
                "pymilvus": types.SimpleNamespace(DataType=mock.Mock(), MilvusClient=mock.Mock())}
with mock.patch.dict(sys.modules, dependencies):
    code = runpy.run_path(str(pathlib.Path(__file__).resolve().parents[1]
                             / "builtin/core/roles/ani/milvus/templates/vector-verify.py"))


class ExistingCollection:
    def __init__(self):
        self.collection_id = 8123
        self.rows = copy.deepcopy(code["ROWS"])
        self.nearest = 101
        self.calls = []

    def describe_collection(self, **arguments):
        self.calls.append(("describe", arguments))
        return {"collection_id": self.collection_id}

    def load_collection(self, **arguments):
        self.calls.append(("load", arguments))

    def query(self, **arguments):
        self.calls.append(("query", arguments))
        return self.rows

    def search(self, **arguments):
        self.calls.append(("search", arguments))
        return [[{"id": self.nearest}]]


class OriginalCollectionReadback(unittest.TestCase):
    def setUp(self):
        self.client = ExistingCollection()
        self.receipt = {"schema": "ani.milvus.persistence.v1", "collection": "ani_verify_0123456789abcdef",
                        "collectionId": 8123, "rowsSha256": code["row_digest"](code["ROWS"])}

    def test_readback_uses_original_identity_and_strong_query_without_write_api(self):
        result = code["readback"](self.client, self.receipt)
        self.assertEqual(result["collectionId"], 8123)
        self.assertEqual([name for name, _ in self.client.calls], ["describe", "load", "query", "search"])
        self.assertEqual(self.client.calls[2][1]["consistency_level"], "Strong")
        self.assertTrue(all(args["collection_name"] == self.receipt["collection"]
                            for _, args in self.client.calls))

    def test_same_name_with_replaced_collection_fails_before_load_or_search(self):
        self.client.collection_id = 9123
        with self.assertRaisesRegex(ValueError, "identity changed"):
            code["readback"](self.client, self.receipt)
        self.assertEqual([name for name, _ in self.client.calls], ["describe"])

    def test_changed_missing_or_extra_vector_data_cannot_pass(self):
        for rows in ([], code["ROWS"][:-1], code["ROWS"] + [{"id": 404, "vector": [1.0, 0.0, 0.0]}],
                     [{"id": 101, "vector": [0.0, 1.0, 0.0]}, *code["ROWS"][1:]]):
            with self.subTest(rows=rows):
                self.client.rows = rows
                with self.assertRaisesRegex(ValueError, "did not survive"):
                    code["readback"](self.client, self.receipt)

    def test_wrong_index_result_cannot_pass(self):
        self.client.nearest = 202
        with self.assertRaisesRegex(ValueError, "index search differs"):
            code["readback"](self.client, self.receipt)

    def test_invalid_or_unrelated_receipts_fail_before_any_api_call(self):
        for change in ({"schema": "other"}, {"collection": "business_data"}, {"collectionId": True},
                       {"collectionId": 0}, {"rowsSha256": "incorrect"}):
            with self.subTest(change=change):
                with self.assertRaisesRegex(ValueError, "receipt differs"):
                    code["readback"](self.client, dict(self.receipt, **change))
        self.assertEqual(self.client.calls, [])


if __name__ == "__main__":
    unittest.main()
