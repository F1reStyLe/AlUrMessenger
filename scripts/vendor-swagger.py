"""Fetch pinned official Swagger UI assets, verify SHA-512, and copy an explicit allowlist.

Only maintainers run this script when updating vendor assets; Go/Docker builds stay offline
with respect to Swagger UI. Archive paths are never extracted directly to the filesystem.
"""
import base64
import hashlib
import io
from pathlib import Path
import tarfile
import urllib.request

VERSION = "5.32.14"
INTEGRITY = "nOA2pSQhcmODMUQZpJHYKNuwniDUqcOWGNaSCOoZv12FdOSJ9JxV95HtyRGNMqEBj6h6lCNTy20TgZDYTSuUIg=="
URL = f"https://registry.npmjs.org/swagger-ui-dist/-/swagger-ui-dist-{VERSION}.tgz"
root = Path(__file__).resolve().parents[1]
destination = root / "internal/platform/apidocs/assets"
with urllib.request.urlopen(URL, timeout=30) as response:
    archive = response.read(32 * 1024 * 1024 + 1)
if len(archive) > 32 * 1024 * 1024 or base64.b64encode(hashlib.sha512(archive).digest()).decode() != INTEGRITY:
    raise SystemExit("Swagger archive integrity mismatch")
with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as bundle:
    files = ["swagger-ui-bundle.js", "swagger-ui.css", "LICENSE", "NOTICE", "swagger-ui-bundle.js.LICENSE.txt"]
    destination.mkdir(parents=True, exist_ok=True)
    for name in files:
        entry = bundle.getmember("package/" + name)
        if not entry.isfile():
            raise SystemExit("Unexpected Swagger archive entry")
        data = bundle.extractfile(entry).read()
        (destination / name).write_bytes(data)
        print(name, len(data), hashlib.sha256(data).hexdigest())
