"""Prepare a NEW 27B directory from checksum-pinned 9B assets; no weights."""
import argparse
import json
from pathlib import Path
import sys
import urllib.request
from qwen35_profile import PROFILE27, ProfileError, pin, sha, validate_exports

def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("manifest", "show", "tags", "version", "output"):
        p.add_argument("--"+name, required=True)
    p.add_argument("--source-dir")
    p.add_argument("--download", action="store_true")
    a = p.parse_args()
    try:
        if bool(a.source_dir) == bool(a.download):
            raise ProfileError("choose_download_or_offline_source")
        raw = Path(a.manifest).read_bytes()
        m = pin(raw, PROFILE27)
        target = Path(a.output)
        target.mkdir(parents=True, exist_ok=False)
        for name, source in m["sources"].items():
            if a.download:
                with urllib.request.urlopen(source["url"], timeout=60) as r:
                    data = r.read(source["bytes"]+1)
            else:
                data = (Path(a.source_dir)/name).read_bytes()
            if sha(data) != source["sha256"] or len(data) != source["bytes"]:
                raise ProfileError("qwen35_resource_checksum_mismatch")
            (target/name).write_bytes(data)
        (target/"manifest.json").write_bytes(raw)
        version = json.loads(Path(a.version).read_text(encoding="utf-8-sig"))["version"]
        validate_exports(target, json.loads(Path(a.show).read_bytes()), json.loads(Path(a.tags).read_bytes()), "ollama version is "+version, PROFILE27)
        print(json.dumps({"profile":PROFILE27,"manifest_sha256":sha(raw),"tokenizer_values_match_pinned_9b":True,"runtime_verified":False}))
        return 0
    except ProfileError as e:
        print(json.dumps({"error":str(e)}))
    except Exception:
        print(json.dumps({"error":"qwen27_preparation_failed"}))
    return 1
if __name__ == "__main__":
    sys.exit(main())
