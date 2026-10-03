#!/usr/bin/env python3
"""Remote read-only actual HTTP/gRPC requests with explicit environment identities."""
import argparse
import hashlib
import http.client
import json
import os
import pathlib
import ssl
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument("--host", required=True)
parser.add_argument("--http-port", type=int, required=True)
parser.add_argument("--grpc-port", type=int, required=True)
parser.add_argument("--ca", required=True)
parser.add_argument("--grpc-client", required=True)
parser.add_argument("--token-a", required=True)
parser.add_argument("--token-b", required=True)
parser.add_argument("--wrong-audience-token", required=True)
parser.add_argument("--run-a", required=True)
parser.add_argument("--run-b", required=True)
parser.add_argument("--output", required=True)
args = parser.parse_args()
output = pathlib.Path(args.output)
output.mkdir(mode=0o700)
report = {"status": "IN_PROGRESS", "persistentRequests": 0, "requests": [],
          "sourceSha256": hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest(),
          "grpcClientSha256": hashlib.sha256(pathlib.Path(args.grpc_client).read_bytes()).hexdigest()}
if report["grpcClientSha256"] != "d1838246f171e155086bafaf99ac352463d2ae16a7a62b691f8318642436e38b":
    raise ValueError("gRPC client differs from approved binary")
context = ssl.create_default_context(cafile=args.ca)
namespaces = ["ani-kfp-probe-a", "ani-kfp-probe-b"]
tokens = [pathlib.Path(p).read_text().strip() for p in (args.token_a, args.token_b)]
wrong = pathlib.Path(args.wrong_audience_token).read_text().strip()
invalid = output / "invalid-token"
invalid.write_text("invalid-environment-probe-token")
os.chmod(invalid, 0o600)

def save():
    body = json.dumps(report, indent=2) + "\n"
    temporary = output / "report.new"
    with temporary.open("x") as stream:
        os.chmod(temporary, 0o600)
        stream.write(body); stream.flush(); os.fsync(stream.fileno())
    os.replace(temporary, output / "report.json")

def http_case(name, namespace, headers, expected, path=None):
    connection = http.client.HTTPSConnection(args.host, args.http_port, context=context, timeout=20)
    try:
        connection.putrequest("GET", path or "/apis/v2beta1/experiments?namespace=" + namespace)
        for key, value in headers:
            connection.putheader(key, value)
        connection.endheaders()
        response = connection.getresponse()
        body = response.read(1048577)
        if len(body) > 1048576:
            raise RuntimeError("HTTP response exceeded bounded size")
        report["requests"].append({"protocol": "HTTP", "case": name, "namespace": namespace,
                                   "status": response.status, "responseSha256": hashlib.sha256(body).hexdigest()})
        save()
        if response.status not in expected:
            raise RuntimeError("HTTP case differs: " + name + " status=" + str(response.status))
    finally:
        connection.close()

def grpc_case(name, namespace, expected, token=None, headers=(), copies=1):
    command = [args.grpc_client, "--endpoint", args.host + ":" + str(args.grpc_port), "--ca", args.ca,
               "--namespace", namespace, "--expect-code", ",".join(expected), "--authorization-copies", str(copies)]
    if token:
        command += ["--token-file", str(token)]
    for key, value in headers:
        command += ["--header", key + "=" + value]
    result = subprocess.run(command, capture_output=True, text=True, timeout=25)
    row = json.loads(result.stdout)
    report["requests"].append({"protocol": "gRPC", "case": name, **row, "exitCode": result.returncode})
    save()
    if result.returncode:
        raise RuntimeError("gRPC case differs: " + name + " status=" + row["status"])

save()
try:
    for index, namespace in enumerate(namespaces):
        token_path = (args.token_a, args.token_b)[index]
        auth = [("Authorization", "Bearer " + tokens[index])]
        http_case("own-list", namespace, auth, (200,))
        http_case("other-list", namespaces[1-index], auth, (403,))
        http_case("own-run", namespace, auth, (200,), "/apis/v2beta1/runs/" + (args.run_a, args.run_b)[index])
        http_case("other-run", namespaces[1-index], auth, (403,), "/apis/v2beta1/runs/" + (args.run_b, args.run_a)[index])
        grpc_case("own-list", namespace, ("OK",), token_path)
        grpc_case("other-list", namespaces[1-index], ("PermissionDenied",), token_path)
    namespace, token_path = namespaces[0], args.token_a
    auth = [("Authorization", "Bearer " + tokens[0])]
    for name, headers in (("missing", []), ("invalid", [("Authorization", "Bearer invalid-environment-probe-token")]),
                          ("wrong-audience", [("Authorization", "Bearer " + wrong)])):
        http_case(name, namespace, headers, (401, 403))
    http_case("duplicate-authorization", namespace, auth + auth, (400, 401, 403))
    grpc_case("missing", namespace, ("Unauthenticated", "PermissionDenied"))
    grpc_case("invalid", namespace, ("Unauthenticated", "PermissionDenied"), invalid)
    grpc_case("wrong-audience", namespace, ("Unauthenticated", "PermissionDenied"), args.wrong_audience_token)
    grpc_case("duplicate-authorization", namespace, ("Internal", "Unauthenticated", "PermissionDenied"), token_path, copies=2)
    for header in ("kubeflow-userid", "x-goog-authenticated-user-email", "x-auth-request-user", "x-auth-request-email", "remote-user", "x-forwarded-user"):
        for variant in (header, header.upper(), header.replace("-", "_")):
            http_case("spoof:" + variant, namespace, auth + [(variant, "forged-probe-b")], (400,))
        http_case("duplicate-spoof:" + header, namespace, auth + [(header, "one"), (header, "two")], (400,))
        for variant in (header, header.replace("-", "_")):
            grpc_case("spoof:" + variant, namespace, ("Internal",), token_path, [(variant, "forged-probe-b")])
        grpc_case("duplicate-spoof:" + header, namespace, ("Internal",), token_path, [(header, "one"), (header, "two")])
    report["status"] = "HTTP_GRPC_IDENTITY_MATRIX_CHECKED"
except BaseException as error:
    report.update(status="FAIL", error=str(error))
    save()
    raise
save()
print(json.dumps({"status": report["status"], "requests": len(report["requests"]), "report": str(output / "report.json")}))
