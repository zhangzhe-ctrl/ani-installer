"""B02 real Milvus and RGW function check, run only from the packaged image."""

import os
import time
import uuid

import boto3
from botocore.config import Config
from pymilvus import DataType, MilvusClient


def required(name):
    value = os.environ.get(name, "")
    if not value:
        raise RuntimeError(f"{name} is required")
    return value


def objects(client, bucket):
    keys = set()
    for page in client.get_paginator("list_objects_v2").paginate(Bucket=bucket, Prefix="files/"):
        keys.update(item["Key"] for item in page.get("Contents", []))
    return keys


def main():
    bucket = required("BUCKET_NAME")
    endpoint = f"http://{required('BUCKET_HOST')}:{required('BUCKET_PORT')}"
    s3 = boto3.client(
        "s3",
        endpoint_url=endpoint,
        aws_access_key_id=required("AWS_ACCESS_KEY_ID"),
        aws_secret_access_key=required("AWS_SECRET_ACCESS_KEY"),
        region_name="us-east-1",
        config=Config(s3={"addressing_style": "path"}),
    )
    before = objects(s3, bucket)
    milvus = MilvusClient(uri=required("MILVUS_URI"))
    collection = "ani_verify_" + uuid.uuid4().hex[:16]
    schema = MilvusClient.create_schema(auto_id=False, enable_dynamic_field=False)
    schema.add_field(field_name="id", datatype=DataType.INT64, is_primary=True)
    schema.add_field(field_name="vector", datatype=DataType.FLOAT_VECTOR, dim=3)
    milvus.create_collection(collection_name=collection, schema=schema)
    inserted = milvus.insert(
        collection_name=collection,
        data=[
            {"id": 101, "vector": [1.0, 0.0, 0.0]},
            {"id": 202, "vector": [0.0, 1.0, 0.0]},
            {"id": 303, "vector": [0.0, 0.0, 1.0]},
        ],
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
        created = objects(s3, bucket) - before
        if created:
            break
        time.sleep(3)
    if not created:
        raise AssertionError("Milvus flush produced no new RGW object under files/")
    key = sorted(created)[0]
    obj = s3.get_object(Bucket=bucket, Key=key)
    sample = obj["Body"].read(1)
    if not sample:
        raise AssertionError("new RGW object could not be read or was empty")
    print(f"ANI-MILVUS-VECTOR-OK collection={collection} nearest_id=101 new_rgws={len(created)}")
    milvus.drop_collection(collection_name=collection)


if __name__ == "__main__":
    main()
