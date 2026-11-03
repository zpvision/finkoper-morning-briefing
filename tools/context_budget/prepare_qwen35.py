"""Explicit preparation only. Never called while processing a diagnostic input."""
import argparse
import json
from pathlib import Path
import sys
import urllib.request

from qwen35_profile import ProfileError, pin, sha, validate_exports


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", required=True)
    parser.add_argument("--show", required=True)
    parser.add_argument("--tags", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--output", required=True, help="new independent directory")
    parser.add_argument("--download", action="store_true", help="explicitly permit downloading pinned source/assets, no weights")
    parser.add_argument("--source-dir", help="offline source cache instead of --download")
    args = parser.parse_args()
    try:
        if bool(args.download) == bool(args.source_dir):
            raise ProfileError("choose_download_or_offline_source")
        raw = Path(args.manifest).read_bytes()
        manifest = pin(raw)
        output = Path(args.output)
        output.mkdir(parents=True, exist_ok=False)
        for name, source in manifest["sources"].items():
            if args.download:
                with urllib.request.urlopen(source["url"], timeout=60) as response:
                    data = response.read(source["bytes"] + 1)
            else:
                data = (Path(args.source_dir) / name).read_bytes()
            if len(data) != source["bytes"] or sha(data) != source["sha256"]:
                raise ProfileError("qwen35_resource_checksum_mismatch")
            (output / name).write_bytes(data)
        (output / "manifest.json").write_bytes(raw)
        validate_exports(output, json.loads(Path(args.show).read_bytes()), json.loads(Path(args.tags).read_bytes()), Path(args.version).read_text(encoding="utf-8-sig"))
        print(json.dumps({"profile": manifest["profile"], "manifest_sha256": sha(raw), "offline_assets_verified": True,
                          "runtime_renderer_verified": False, "runtime_token_count_verified": False}))
        return 0
    except ProfileError as error:
        print(json.dumps({"error": str(error)}))
    except Exception:
        print(json.dumps({"error": "qwen35_preparation_failed"}))
    return 1


if __name__ == "__main__":
    sys.exit(main())
