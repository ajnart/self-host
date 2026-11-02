import hashlib
import json
import os
import pathlib
import platform
import re
import subprocess
import sys
import urllib.request

os.umask(0o077)
destination = pathlib.Path(sys.argv[1])
version = "go1.27.3"
system = platform.system().lower()
architecture = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(
    platform.machine().lower()
)
if system not in {"linux", "darwin"} or architecture is None:
    sys.exit("The testkit Action requires Linux or macOS on x64 or arm64.")

filename = f"{version}.{system}-{architecture}.tar.gz"
with urllib.request.urlopen(
    "https://go.dev/dl/?mode=json&include=all", timeout=60
) as response:
    metadata = response.read(5 * 1024 * 1024 + 1)
if len(metadata) > 5 * 1024 * 1024:
    sys.exit("Go release metadata exceeds its size limit.")
files = [
    file
    for release in json.loads(metadata)
    if release["version"] == version
    for file in release["files"]
    if file["filename"] == filename
]
if len(files) != 1 or not re.fullmatch(r"[a-f0-9]{64}", files[0]["sha256"]):
    sys.exit("Go release metadata lacks an unambiguous toolchain checksum.")

archive = destination / filename
digest = hashlib.sha256()
size = 0
with (
    urllib.request.urlopen(f"https://go.dev/dl/{filename}", timeout=60) as response,
    archive.open("wb") as output,
):
    while True:
        chunk = response.read(1024 * 1024)
        if not chunk:
            break
        size += len(chunk)
        if size > 128 * 1024 * 1024:
            sys.exit("Go toolchain archive exceeds its size limit.")
        digest.update(chunk)
        output.write(chunk)
if digest.hexdigest() != files[0]["sha256"]:
    sys.exit("Go toolchain checksum mismatch; refusing extraction.")
subprocess.run(
    ["tar", "-xzf", str(archive), "-C", str(destination)], check=True, timeout=60
)
