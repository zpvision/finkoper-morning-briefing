"""Copy and verify an existing tokenizer bundle, without downloads or model loading.

Inputs are read-only. Output must be a new, separate directory belonging to briefing.
"""

import argparse
import hashlib
import json
from pathlib import Path
import re
import sys
import tarfile


class ResourceError(Exception):
    pass


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def validate_bundle(archive, show_path):
    names = ("manifest.json", "tokenizer.json", "tokenizer_config.json")
    with tarfile.open(archive, "r:gz") as bundle:
        files = {}
        for name in names:
            member = bundle.getmember("qwen3-8b/" + name)
            if not member.isfile() or member.size > 32 * 1024 * 1024:
                raise ResourceError("invalid_bundle_member")
            with bundle.extractfile(member) as source:
                files[name] = source.read(32 * 1024 * 1024 + 1)
    manifest = json.loads(files["manifest.json"])
    if manifest.get("repository") != "Qwen/Qwen3-8B":
        raise ResourceError("unexpected_repository")
    if not re.fullmatch(r"[0-9a-f]{40}", manifest.get("revision", "")):
        raise ResourceError("invalid_revision")
    if set(manifest.get("files", {})) != set(names[1:]):
        raise ResourceError("invalid_manifest_files")
    for name in names[1:]:
        if sha256(files[name]) != manifest["files"][name]:
            raise ResourceError("tokenizer_checksum_mismatch")
    if not re.fullmatch(r"[0-9a-f]{64}", manifest.get("model_digest", "")):
        raise ResourceError("invalid_model_digest")

    show = json.loads(Path(show_path).read_bytes())
    template = show.get("template")
    if not isinstance(template, str):
        raise ResourceError("template_missing")
    if sha256(template.encode("utf-8")) != manifest.get("ollama_template_sha256"):
        raise ResourceError("template_checksum_mismatch")
    info = show.get("model_info", {})
    if (info.get("general.architecture") != "qwen3"
            or info.get("tokenizer.ggml.model") != "gpt2"
            or info.get("tokenizer.ggml.pre") != "qwen2"
            or info.get("tokenizer.ggml.add_bos_token") is not False):
        raise ResourceError("unsupported_tokenizer_profile")
    tokenizer = json.loads(files["tokenizer.json"])
    model = tokenizer.get("model", {})
    if model.get("type") != "BPE":
        raise ResourceError("unsupported_tokenizer_algorithm")
    tokens = info.get("tokenizer.ggml.tokens", [])
    types = info.get("tokenizer.ggml.token_type", [])
    merges = info.get("tokenizer.ggml.merges", [])
    if not tokens or len(tokens) != len(types) or not merges:
        raise ResourceError("incomplete_ollama_vocabulary")
    for text, index in model.get("vocab", {}).items():
        if type(index) is not int or not 0 <= index < len(tokens) or tokens[index] != text:
            raise ResourceError("vocabulary_mismatch")
    for added in tokenizer.get("added_tokens", []):
        index = added["id"]
        if not 0 <= index < len(tokens) or tokens[index] != added["content"]:
            raise ResourceError("added_token_mismatch")
    local_merges = [" ".join(pair) if isinstance(pair, list) else pair
                    for pair in model.get("merges", [])]
    if local_merges != merges:
        raise ResourceError("merges_mismatch")
    summary = {
        "manifest_sha256": sha256(files["manifest.json"]),
        "model_digest": manifest["model_digest"],
        "template_sha256": manifest["ollama_template_sha256"],
        "vocabulary_entries": len(tokens),
        "bpe_vocabulary_entries": len(model["vocab"]),
        "merges": len(merges),
        "runtime_compatibility_verified": False,
    }
    return files, summary


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", required=True)
    parser.add_argument("--show", required=True)
    parser.add_argument("--output", help="new directory; omit for validation only")
    args = parser.parse_args()
    try:
        files, summary = validate_bundle(args.archive, args.show)
        if args.output:
            output = Path(args.output)
            output.mkdir(parents=True, exist_ok=False)
            for name, data in files.items():
                (output / name).write_bytes(data)
        print(json.dumps(summary, ensure_ascii=True))
    except ResourceError as error:
        print(json.dumps({"error": str(error)}), file=sys.stderr)
        return 1
    except (OSError, ValueError, KeyError, TypeError, tarfile.TarError):
        # Do not print arbitrary exception strings or source file contents.
        print(json.dumps({"error": "resource_preparation_failed"}), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
