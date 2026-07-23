#!/usr/bin/env python3
"""Small, dependency-free helpers for the root release bootstrap.

Shell is a poor JSON parser and GNU tar's permissive extraction defaults are not
an acceptable trust boundary.  Ubuntu 24.04 ships Python 3, so the installer
uses its standard library for these two narrowly scoped operations.
"""

from __future__ import annotations

import json
import os
import re
import shutil
import stat
import sys
import tarfile
from pathlib import Path, PurePosixPath
from urllib.parse import urlparse


MAX_ARCHIVE_BYTES = 512 * 1024 * 1024
MAX_EXPANDED_BYTES = 1024 * 1024 * 1024
MAX_MEMBERS = 4096
ASSET_PATH = re.compile(r"^/repos/[^/]+/[^/]+/releases/assets/[0-9]+$")
PROXY_INCLUDE = "include /etc/nginx/snippets/nexa-panel-proxy.conf;"


class ReleaseError(ValueError):
    pass


def asset_url(metadata_path: Path, wanted_name: str) -> str:
    with metadata_path.open("r", encoding="utf-8") as source:
        metadata = json.load(source)
    assets = metadata.get("assets")
    if not isinstance(assets, list):
        raise ReleaseError("release metadata has no assets array")
    matches = [asset for asset in assets if isinstance(asset, dict) and asset.get("name") == wanted_name]
    if len(matches) != 1:
        raise ReleaseError(f"release must contain exactly one asset named {wanted_name!r}")
    url = matches[0].get("url")
    if not isinstance(url, str):
        raise ReleaseError(f"asset {wanted_name!r} has no API URL")
    parsed = urlparse(url)
    if parsed.scheme != "https" or parsed.hostname != "api.github.com" or parsed.query or parsed.fragment:
        raise ReleaseError(f"asset {wanted_name!r} has an untrusted API URL")
    if not ASSET_PATH.fullmatch(parsed.path):
        raise ReleaseError(f"asset {wanted_name!r} has an unexpected API path")
    return url


def safe_relative_path(name: str) -> PurePosixPath:
    if not name or "\x00" in name or "\\" in name:
        raise ReleaseError(f"unsafe archive path {name!r}")
    path = PurePosixPath(name)
    if path.is_absolute() or any(part in ("", ".", "..") for part in path.parts):
        raise ReleaseError(f"unsafe archive path {name!r}")
    return path


def extract_release(archive_path: Path, destination: Path) -> Path:
    archive_size = archive_path.stat().st_size
    if archive_size <= 0 or archive_size > MAX_ARCHIVE_BYTES:
        raise ReleaseError("release archive is empty or exceeds the 512 MiB limit")
    if destination.exists():
        raise ReleaseError("release extraction destination already exists")
    destination.mkdir(mode=0o700, parents=True)

    total_size = 0
    seen: set[PurePosixPath] = set()
    top_levels: set[str] = set()
    entries: list[tuple[tarfile.TarInfo, PurePosixPath]] = []
    try:
        with tarfile.open(archive_path, mode="r:gz") as archive:
            members = archive.getmembers()
            if not members or len(members) > MAX_MEMBERS:
                raise ReleaseError("release archive has no entries or too many entries")
            for member in members:
                relative = safe_relative_path(member.name.rstrip("/"))
                if relative in seen:
                    raise ReleaseError(f"duplicate archive entry {member.name!r}")
                seen.add(relative)
                top_levels.add(relative.parts[0])
                if not (member.isdir() or member.isreg()):
                    raise ReleaseError(f"unsupported archive entry type for {member.name!r}")
                if member.size < 0:
                    raise ReleaseError(f"negative archive entry size for {member.name!r}")
                total_size += member.size
                if total_size > MAX_EXPANDED_BYTES:
                    raise ReleaseError("release archive exceeds the 1 GiB expanded-size limit")
                entries.append((member, relative))
            if len(top_levels) != 1:
                raise ReleaseError("release archive must contain exactly one top-level directory")
            root_name = next(iter(top_levels))
            if not re.fullmatch(r"nexa-panel-[A-Za-z0-9._+-]+-linux-(amd64|arm64)", root_name):
                raise ReleaseError("release archive has an unexpected top-level directory")

            for member, relative in entries:
                target = destination.joinpath(*relative.parts)
                if member.isdir():
                    target.mkdir(mode=0o755, parents=True, exist_ok=True)
                    os.chmod(target, member.mode & 0o755 or 0o755)
                    continue
                target.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
                source = archive.extractfile(member)
                if source is None:
                    raise ReleaseError(f"cannot read archive entry {member.name!r}")
                flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
                if hasattr(os, "O_NOFOLLOW"):
                    flags |= os.O_NOFOLLOW
                descriptor = os.open(target, flags, member.mode & 0o777 or 0o600)
                with source, os.fdopen(descriptor, "wb") as output:
                    shutil.copyfileobj(source, output, length=1024 * 1024)
                    output.flush()
                    os.fsync(output.fileno())
                os.chmod(target, member.mode & 0o777)

        root = destination / next(iter(top_levels))
        required = {
            "bin/nexa": True,
            "scripts/install.sh": True,
            "scripts/nexa-seed-admin.sh": True,
            "scripts/nexa-release-helper.py": False,
            "packaging/systemd/nexa-api.service": False,
            "packaging/systemd/nexa-agent.service": False,
            "packaging/nginx/nexa-panel.conf.template": False,
            "RELEASE": False,
        }
        for relative, executable in required.items():
            path = root / relative
            if not path.is_file():
                raise ReleaseError(f"release bundle is missing {relative}")
            if executable and not os.access(path, os.X_OK):
                raise ReleaseError(f"release bundle file {relative} is not executable")
        release_mode = stat.S_IMODE((root / "RELEASE").stat().st_mode)
        if release_mode & 0o022:
            raise ReleaseError("release metadata is group- or world-writable")
        return root / "scripts/install.sh"
    except Exception:
        shutil.rmtree(destination, ignore_errors=True)
        raise


def brace_delta(line: str) -> int:
    """Count structural braces while ignoring quoted strings and comments."""
    delta = 0
    quote = ""
    escaped = False
    for character in line:
        if escaped:
            escaped = False
            continue
        if quote and character == "\\":
            escaped = True
            continue
        if character == quote:
            quote = ""
            continue
        if quote:
            continue
        if character in ("'", '"'):
            quote = character
        elif character == "#":
            break
        elif character == "{":
            delta += 1
        elif character == "}":
            delta -= 1
    return delta


def migrate_nginx_vhost(input_path: Path, output_path: Path) -> None:
    page = input_path.read_text(encoding="utf-8")
    if PROXY_INCLUDE in page:
        output_path.write_text(page, encoding="utf-8")
        return
    lines = page.splitlines(keepends=True)
    depth = 0
    server_ranges: list[tuple[int, int]] = []
    start: int | None = None
    for index, line in enumerate(lines):
        before = depth
        delta = brace_delta(line)
        if before == 0 and re.match(r"^\s*server\s*\{", line):
            start = index
        depth += delta
        if depth < 0:
            raise ReleaseError("Nginx vhost has unmatched closing braces")
        if start is not None and depth == 0:
            server_ranges.append((start, index))
            start = None
    if depth != 0 or start is not None:
        raise ReleaseError("Nginx vhost has unmatched braces")
    candidates = [bounds for bounds in server_ranges if "/run/nexa-panel/api.sock" in "".join(lines[bounds[0] : bounds[1] + 1])]
    if len(candidates) != 1:
        raise ReleaseError("expected exactly one Nexa API server block in the legacy vhost")
    server_start, server_end = candidates[0]
    block = lines[server_start : server_end + 1]
    result = [block[0]]
    relative_depth = 1
    removed_locations = 0
    index = 1
    location_pattern = re.compile(r"^\s*location\s+(?:=\s+)?(?:/metrics|/api/v1/auth/|/)\s*\{")
    directive_pattern = re.compile(r"^\s*(?:limit_req_status|client_max_body_size|client_body_timeout)\s+")
    while index < len(block) - 1:
        line = block[index]
        if relative_depth == 1 and directive_pattern.match(line):
            index += 1
            continue
        if relative_depth == 1 and location_pattern.match(line):
            location_depth = brace_delta(line)
            if location_depth <= 0:
                raise ReleaseError("legacy Nexa location block is malformed")
            index += 1
            while index < len(block) and location_depth > 0:
                location_depth += brace_delta(block[index])
                index += 1
            if location_depth != 0:
                raise ReleaseError("legacy Nexa location block has unmatched braces")
            removed_locations += 1
            continue
        result.append(line)
        relative_depth += brace_delta(line)
        index += 1
    if removed_locations != 3:
        raise ReleaseError(f"expected three legacy Nexa proxy locations, found {removed_locations}")
    result.append("    # Managed separately so proxy policy updates do not rewrite TLS directives.\n")
    result.append(f"    {PROXY_INCLUDE}\n")
    result.append(block[-1])
    migrated = lines[:server_start] + result + lines[server_end + 1 :]
    output_path.write_text("".join(migrated), encoding="utf-8")


def main(argv: list[str]) -> int:
    try:
        if len(argv) == 4 and argv[1] == "asset-url":
            print(asset_url(Path(argv[2]), argv[3]))
            return 0
        if len(argv) == 4 and argv[1] == "extract":
            print(extract_release(Path(argv[2]), Path(argv[3])))
            return 0
        if len(argv) == 4 and argv[1] == "migrate-nginx-vhost":
            migrate_nginx_vhost(Path(argv[2]), Path(argv[3]))
            return 0
        raise ReleaseError("usage: nexa-release-helper.py asset-url METADATA NAME | extract ARCHIVE DESTINATION | migrate-nginx-vhost INPUT OUTPUT")
    except (OSError, json.JSONDecodeError, tarfile.TarError, ReleaseError) as error:
        print(f"release helper: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
