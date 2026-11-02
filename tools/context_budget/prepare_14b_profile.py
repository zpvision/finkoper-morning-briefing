"""Offline proof and separate resource copy for the diagnostic 14B profile.

No network, weights or changes to source resources. Compare complete exported
tokenizer metadata, then bind it to the existing pinned HF tokenizer assets.
"""
import argparse
import json
from pathlib import Path
import re
import sys

from prepare_resources import ResourceError, sha256

PROFILE = "qwen3-14b-q4-k-m-v1"
MODEL = "qwen3:14b-q4_K_M"


def canonical(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=True, separators=(",", ":")).encode()


def prepare(source, source_show, target_show, tags):
    source = Path(source)
    manifest = json.loads((source / "manifest.json").read_bytes())
    if (manifest.get("repository") != "Qwen/Qwen3-8B"
            or manifest.get("diagnostic_profile") is not None
            or not re.fullmatch(r"[a-f0-9]{40}", manifest.get("revision", ""))):
        raise ResourceError("invalid_source_manifest")
    files = {name: (source / name).read_bytes() for name in ("tokenizer.json", "tokenizer_config.json")}
    if set(manifest.get("files", {})) != set(files):
        raise ResourceError("invalid_manifest_files")
    for name, data in files.items():
        if sha256(data) != manifest["files"][name]:
            raise ResourceError("tokenizer_checksum_mismatch")
    old = json.loads(Path(source_show).read_bytes())
    new = json.loads(Path(target_show).read_bytes())
    old_info, info = old["model_info"], new["model_info"]
    old_metadata = {k: v for k, v in old_info.items() if k.startswith("tokenizer.")}
    metadata = {k: v for k, v in info.items() if k.startswith("tokenizer.")}
    if not metadata or old_metadata != metadata:
        raise ResourceError("exported_tokenizer_metadata_mismatch")
    if (old_info.get("general.architecture") != "qwen3"
            or info.get("general.architecture") != "qwen3"
            or old_info.get("general.parameter_count") != 8190735360
            or info.get("general.parameter_count") != 14768307200
            or new.get("details", {}).get("quantization_level") != "Q4_K_M"
            or new.get("details", {}).get("format") != "gguf"
            or metadata.get("tokenizer.ggml.model") != "gpt2"
            or metadata.get("tokenizer.ggml.pre") != "qwen2"
            or metadata.get("tokenizer.ggml.add_bos_token") is not False):
        raise ResourceError("unsupported_14b_profile")
    template = new.get("template")
    if (not isinstance(template, str) or template != old.get("template")
            or sha256(template.encode()) != manifest.get("ollama_template_sha256")):
        raise ResourceError("template_mismatch")
    tokenizer = json.loads(files["tokenizer.json"])
    config = json.loads(files["tokenizer_config.json"])
    model = tokenizer["model"]
    tokens, types, merges = (metadata["tokenizer.ggml." + key] for key in ("tokens", "token_type", "merges"))
    if (model.get("type") != "BPE" or len(tokens) != 151936 or len(types) != len(tokens)
            or len(merges) != 151387 or len(model["vocab"]) != 151643):
        raise ResourceError("incomplete_vocabulary")
    for text, index in model["vocab"].items():
        if type(index) is not int or not 0 <= index < len(tokens) or tokens[index] != text:
            raise ResourceError("vocabulary_mismatch")
    local_merges = [" ".join(pair) if isinstance(pair, list) else pair for pair in model["merges"]]
    if local_merges != merges:
        raise ResourceError("merges_mismatch")
    added = tokenizer["added_tokens"]
    for token in added:
        index = token["id"]
        if (type(index) is not int or not 0 <= index < len(tokens) or tokens[index] != token["content"]
                or (token.get("special") is True and types[index] != 3)):
            raise ResourceError("special_token_mismatch")
        configured = config.get("added_tokens_decoder", {}).get(str(index))
        if not isinstance(configured, dict) or any(configured.get(k) != token.get(k) for k in ("content", "special", "normalized", "lstrip", "rstrip", "single_word")):
            raise ResourceError("special_token_config_mismatch")
    if config.get("add_bos_token") is not False or config.get("bos_token") is not None:
        raise ResourceError("bos_policy_mismatch")
    for name, configured in (("eos", "eos_token"), ("padding", "pad_token")):
        index = metadata["tokenizer.ggml." + name + "_token_id"]
        if type(index) is not int or not 0 <= index < len(tokens) or config.get(configured) != tokens[index]:
            raise ResourceError("special_token_id_mismatch")
    entries = json.loads(Path(tags).read_bytes())["models"]
    target = [entry for entry in entries if entry.get("name") == MODEL or entry.get("model") == MODEL]
    original = [entry for entry in entries if entry.get("name") == "qwen3:8b" or entry.get("model") == "qwen3:8b"]
    if (len(target) != 1 or len(original) != 1
            or original[0].get("digest") != manifest["model_digest"]
            or not re.fullmatch(r"[a-f0-9]{64}", target[0].get("digest", ""))
            or target[0].get("details", {}).get("quantization_level") != "Q4_K_M"
            or target[0].get("details", {}).get("parameter_size") != "14.8B"):
        raise ResourceError("model_tags_mismatch")
    manifest["source_model_digest"] = manifest["model_digest"]
    manifest["model_digest"] = target[0]["digest"]
    manifest["model_name"] = MODEL
    manifest["diagnostic_profile"] = PROFILE
    manifest["compatibility_evidence"] = {
        "tokenizer_metadata_sha256": sha256(canonical(metadata)),
        "vocabulary_entries": len(tokens), "bpe_vocabulary_entries": len(model["vocab"]),
        "merges": len(merges), "added_tokens": len(added),
        "added_tokens_sha256": sha256(canonical(added)),
        "all_exported_tokenizer_fields_equal": True,
        "templates_byte_equal": True,
    }
    files["manifest.json"] = (json.dumps(manifest, indent=2, ensure_ascii=True) + "\n").encode()
    return files


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True)
    parser.add_argument("--source-show", required=True)
    parser.add_argument("--target-show", required=True)
    parser.add_argument("--tags", required=True)
    parser.add_argument("--expected-manifest", help="compare computed profile to the committed pin")
    parser.add_argument("--manifest-output", help="initial preparation only; create new manifest pin")
    parser.add_argument("--output", help="new separate resource directory; omit for validation only")
    args = parser.parse_args()
    try:
        files = prepare(args.source, args.source_show, args.target_show, args.tags)
        if args.expected_manifest and Path(args.expected_manifest).read_bytes() != files["manifest.json"]:
            raise ResourceError("diagnostic_profile_pin_mismatch")
        if args.manifest_output:
            path = Path(args.manifest_output)
            path.parent.mkdir(parents=True, exist_ok=True)
            with path.open("xb") as stream:
                stream.write(files["manifest.json"])
        if args.output:
            output = Path(args.output)
            output.mkdir(parents=True, exist_ok=False)
            for name, data in files.items():
                (output / name).write_bytes(data)
        manifest = json.loads(files["manifest.json"])
        print(json.dumps({"profile": PROFILE, "model": MODEL, "manifest_sha256": sha256(files["manifest.json"]),
                          "digest": manifest["model_digest"], "template_sha256": manifest["ollama_template_sha256"],
                          "evidence": manifest["compatibility_evidence"], "runtime_verified": False}))
        return 0
    except ResourceError as error:
        print(json.dumps({"error": str(error)}), file=sys.stderr)
    except (OSError, ValueError, KeyError, TypeError):
        print(json.dumps({"error": "profile_preparation_failed"}), file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
