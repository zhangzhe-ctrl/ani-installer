"""Real Milvus vector flow with a verified HTTPS S3 backend.

The packaged B02 checker image supplies pinned PyMilvus and Boto3. The Job
mounts this reviewed script so both retained RGW and RustFS use the same flow.
"""

import argparse
import hashlib
import json
import os
import pathlib
import re
import time
import uuid

import boto3
from botocore.config import Config
from pymilvus import DataType, MilvusClient

ROWS = [
    {"id": 101, "vector": [1.0, 0.0, 0.0]},
    {"id": 202, "vector": [0.0, 1.0, 0.0]},
    {"id": 303, "vector": [0.0, 0.0, 1.0]},
]


def row_digest(rows):
    return hashlib.sha256(json.dumps(rows, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def readback(milvus, receipt):
    """Read the original collection after recreation; never insert new rows."""
    if (receipt.get("schema") != "ani.milvus.persistence.v1"
            or not re.fullmatch(r"ani_verify_[0-9a-f]{16}", receipt.get("collection", ""))
            or type(receipt.get("collectionId")) is not int or receipt["collectionId"] <= 0
            or receipt.get("rowsSha256") != row_digest(ROWS)):
        raise ValueError("Milvus persistence receipt differs")
    name = receipt["collection"]
    current = milvus.describe_collection(collection_name=name, timeout=120)
    if current["collection_id"] != receipt["collectionId"]:
        raise ValueError("original Milvus collection identity changed")
    milvus.load_collection(collection_name=name, timeout=120)
    rows = sorted(milvus.query(collection_name=name, filter="id >= 0", limit=4,
                              output_fields=["id", "vector"], consistency_level="Strong", timeout=120),
                  key=lambda row: row["id"])
    if rows != ROWS or row_digest(rows) != receipt["rowsSha256"]:
        raise ValueError("original Milvus vector rows did not survive")
    found = milvus.search(collection_name=name, data=[[0.99, 0.01, 0.0]], limit=1,
                          search_params={"metric_type": "L2", "params": {}}, timeout=120)
    if not found or not found[0] or found[0][0]["id"] != 101:
        raise ValueError("original Milvus index search differs")
    return {"collection": name, "collectionId": receipt["collectionId"],
            "rowsSha256": row_digest(rows), "nearestId": 101}


def required(name):
    value = os.environ.get(name, "")
    if not value:
        raise RuntimeError(f"{name} is required")
    return value


def objects(client, bucket, prefix):
    keys = set()
    for page in client.get_paginator("list_objects_v2").paginate(
        Bucket=bucket, Prefix=prefix
    ):
        keys.update(item["Key"] for item in page.get("Contents", []))
    return keys


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("smoke", "write", "readback", "cleanup"), default="smoke")
    parser.add_argument("--receipt", type=pathlib.Path)
    args = parser.parse_args()
    if (args.mode != "smoke") != (args.receipt is not None):
        raise ValueError("persistence phases require an explicit receipt path")
    if args.mode in ("readback", "cleanup"):
        milvus = MilvusClient(uri=required("MILVUS_URI"))
        receipt = json.loads(args.receipt.read_text())
        result = readback(milvus, receipt)
        if args.mode == "cleanup":
            milvus.drop_collection(collection_name=receipt["collection"])
        print("ANI-MILVUS-PERSISTENCE-" + args.mode.upper() + " " + json.dumps(result, sort_keys=True))
        return
    if args.receipt and args.receipt.exists():
        raise ValueError("refusing to replace an existing persistence receipt")
    endpoint = required("S3_ENDPOINT")
    ca_path = required("S3_CA_PATH")
    if not endpoint.startswith("https://") or not os.path.isfile(ca_path):
        raise RuntimeError("the selected S3 binding requires a mounted HTTPS CA")
    bucket = required("S3_BUCKET")
    prefix = required("S3_ROOT_PATH").strip("/") + "/"
    s3 = boto3.client(
        "s3",
        endpoint_url=endpoint,
        aws_access_key_id=required("AWS_ACCESS_KEY_ID"),
        aws_secret_access_key=required("AWS_SECRET_ACCESS_KEY"),
        region_name=required("AWS_DEFAULT_REGION"),
        verify=ca_path,
        config=Config(s3={"addressing_style": "path"}),
    )
    before = objects(s3, bucket, prefix)
    milvus = MilvusClient(uri=required("MILVUS_URI"))
    collection = "ani_verify_" + uuid.uuid4().hex[:16]
    schema = MilvusClient.create_schema(auto_id=False, enable_dynamic_field=False)
    schema.add_field(field_name="id", datatype=DataType.INT64, is_primary=True)
    schema.add_field(field_name="vector", datatype=DataType.FLOAT_VECTOR, dim=3)
    milvus.create_collection(collection_name=collection, schema=schema)
    inserted = milvus.insert(
        collection_name=collection,
        data=ROWS,
    )
    if inserted.get("insert_count") != 3:
        raise AssertionError(f"Milvus inserted {inserted.get('insert_count')} rows, want 3")
    milvus.flush_all(timeout=120)
    params = milvus.prepare_index_params()
    params.add_index(field_name="vector", index_type="FLAT", metric_type="L2")
    milvus.create_index(collection_name=collection, index_params=params, timeout=120)
    milvus.load_collection(collection_name=collection, timeout=120)
    milvus.release_collection(collection_name=collection)
    milvus.load_collection(collection_name=collection, timeout=120)
    found = milvus.search(
        collection_name=collection,
        data=[[0.99, 0.01, 0.0]],
        limit=1,
        search_params={"metric_type": "L2", "params": {}},
        timeout=120,
    )
    if not found or not found[0] or found[0][0]["id"] != 101:
        raise AssertionError(f"Milvus nearest-neighbor mismatch: {found!r}")
    deadline = time.monotonic() + 90
    created = set()
    while time.monotonic() < deadline:
        created = objects(s3, bucket, prefix) - before
        if created:
            break
        time.sleep(3)
    if not created:
        raise AssertionError("Milvus flush produced no new object under the selected rootPath")
    key = sorted(created)[0]
    obj = s3.get_object(Bucket=bucket, Key=key)
    if not obj["Body"].read(1):
        raise AssertionError("a new Milvus object could not be read or was empty")
    if args.mode == "write":
        description = milvus.describe_collection(collection_name=collection, timeout=120)
        receipt = {"schema": "ani.milvus.persistence.v1", "collection": collection,
                   "collectionId": description["collection_id"], "rowsSha256": row_digest(ROWS)}
        readback(milvus, receipt)
        with args.receipt.open("x") as stream:
            json.dump(receipt, stream, sort_keys=True)
        print("ANI-MILVUS-WRITE-RECEIPT " + json.dumps(receipt, sort_keys=True))
        return
    print(
        f"ANI-MILVUS-VECTOR-OK collection={collection} nearest_id=101 "
        f"new_objects={len(created)} provider={required('S3_PROVIDER')}"
    )
    milvus.drop_collection(collection_name=collection)


if __name__ == "__main__":
    main()
