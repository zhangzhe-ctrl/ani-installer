"""Fixed CPU model, executed by the real Notebook kernel during acceptance."""
import hashlib
import json
import os
import pathlib

INPUT = [[0.0, 0.0], [10.0, 10.0], [20.0, 20.0]]
EXPECTED = [0, 1, 2]


def generate(directory):
    import joblib
    import sklearn
    from sklearn.tree import DecisionTreeClassifier

    if sklearn.__version__ != "1.5.2" or joblib.__version__ != "1.4.2":
        raise RuntimeError("training serializer versions differ from the fixed Runtime contract")
    directory = pathlib.Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    estimator = DecisionTreeClassifier(max_depth=2, random_state=42)
    estimator.fit([[0.0, 0.0], [1.0, 1.0], [9.0, 9.0], [10.0, 10.0], [19.0, 19.0], [20.0, 20.0]],
                  [0, 0, 1, 1, 2, 2])
    actual = estimator.predict(INPUT).tolist()
    if actual != EXPECTED:
        raise RuntimeError("fixed training prediction differs")
    path = directory / "model.joblib"
    joblib.dump(estimator, path)
    record = {"format": "joblib", "sklearn": sklearn.__version__, "joblib": joblib.__version__,
              "random_seed": 42, "file": path.name, "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
              "input": INPUT, "expected": EXPECTED}
    (directory / "model-contract.json").write_text(json.dumps(record, indent=2) + "\n")
    return record


def upload(directory, key):
    import boto3
    from botocore.config import Config

    prefix = os.environ["ANI_MODEL_PREFIX"]
    if not key.startswith(prefix + "/") or pathlib.PurePosixPath(key).name != "model.joblib":
        raise ValueError("model object is outside the dedicated prefix")
    endpoint = os.environ["ANI_S3_ENDPOINT"]
    if not endpoint.startswith("https://"):
        raise ValueError("model upload requires HTTPS")
    ca = pathlib.Path(os.environ["AWS_CA_BUNDLE"])
    if not ca.is_file():
        raise ValueError("model upload requires the mounted public CA")
    client = boto3.client("s3", endpoint_url=endpoint, region_name="us-east-1", verify=str(ca),
                          config=Config(signature_version="s3v4", s3={"addressing_style": "path"}))
    path = pathlib.Path(directory) / "model.joblib"
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    client.put_object(Bucket=os.environ["ANI_MODEL_BUCKET"], Key=key, Body=path.read_bytes(),
                      Metadata={"sha256": digest})
    return {"bucket": os.environ["ANI_MODEL_BUCKET"], "key": key, "sha256": digest}
