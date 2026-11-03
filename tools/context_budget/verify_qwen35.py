"""Read-only verification of existing pinned Qwen3.5 resources; no network or weights."""
import argparse
import json
from pathlib import Path
import sys

from qwen35_profile import ProfileError, load_files, sha, validate_exports


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--resources", required=True)
    parser.add_argument("--show")
    parser.add_argument("--tags")
    parser.add_argument("--version")
    args = parser.parse_args(argv)
    try:
        exports = (args.show, args.tags, args.version)
        if any(exports) and not all(exports):
            raise ProfileError("qwen35_verification_requires_all_exports")
        manifest, files = load_files(args.resources)
        if all(exports):
            validate_exports(args.resources, json.loads(Path(args.show).read_bytes()),
                             json.loads(Path(args.tags).read_bytes()),
                             Path(args.version).read_text(encoding="utf-8-sig"))
        print(json.dumps({"profile": manifest["profile"], "model": manifest["model"],
                          "manifest_sha256": sha((Path(args.resources) / "manifest.json").read_bytes()),
                          "files_verified": len(files), "offline_assets_verified": True,
                          "exports_verified": all(exports), "runtime_renderer_verified": False,
                          "runtime_token_count_verified": False}))
        return 0
    except ProfileError as error:
        print(json.dumps({"error": str(error)}))
    except Exception:
        print(json.dumps({"error": "qwen35_resource_verification_failed"}))
    return 1


if __name__ == "__main__":
    sys.exit(main())
