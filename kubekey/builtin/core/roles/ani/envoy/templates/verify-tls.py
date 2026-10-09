#!/usr/bin/env python3
"""Validate certgen's actual CA, service SAN, expiry and private-key pair."""
import base64
import json
import pathlib
import subprocess
import sys
import tempfile

secret = json.load(sys.stdin)
if secret.get("type") != "kubernetes.io/tls":
    raise ValueError("Envoy certgen did not produce a TLS Secret")
data = secret.get("data", {})
if not all(key in data for key in ("ca.crt", "tls.crt", "tls.key")):
    raise ValueError("Envoy TLS Secret lacks CA/certificate/key")
with tempfile.TemporaryDirectory(prefix="ani-envoy-public-cert-") as temporary:
    ca, certificate = pathlib.Path(temporary) / "ca.crt", pathlib.Path(temporary) / "tls.crt"
    ca.write_bytes(base64.b64decode(data["ca.crt"], validate=True))
    certificate.write_bytes(base64.b64decode(data["tls.crt"], validate=True))
    subprocess.run(["openssl", "verify", "-CAfile", str(ca), "-verify_hostname",
                    "envoy-gateway.envoy-gateway-system.svc", str(certificate)], check=True, capture_output=True)
    subprocess.run(["openssl", "x509", "-in", str(certificate), "-noout", "-checkend", "3600"], check=True, capture_output=True)
    public = subprocess.check_output(["openssl", "x509", "-in", str(certificate), "-pubkey", "-noout"])
    private_public = subprocess.run(["openssl", "pkey", "-pubout"],
        input=base64.b64decode(data["tls.key"], validate=True), check=True, capture_output=True).stdout
    if public != private_public:
        raise ValueError("Envoy TLS Secret key does not match its certificate")
print("Envoy certgen CA, service SAN, expiry and key pair verified")
