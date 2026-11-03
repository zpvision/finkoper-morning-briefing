"""Pinned Qwen3.5 resources and text-only renderer; independent of the 8B helper."""
import hashlib
import json
from pathlib import Path
import re

PROFILE = "qwen35-9b-summary-v2-v1"
PIN_SHA256 = "e9aeb0ccd5b2d1f2f952ba1486055c57e8635a93c693c3924b23904ddf9e1e51"
METHOD = "qwen35_tokenizers_pinned_renderer_no_normalization"


class ProfileError(Exception):
    pass


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def pin(raw):
    if sha(raw) != PIN_SHA256:
        raise ProfileError("qwen35_manifest_pin_mismatch")
    return json.loads(raw)


def load_files(directory):
    directory = Path(directory)
    manifest = pin((directory / "manifest.json").read_bytes())
    files = {}
    for name, source in manifest["sources"].items():
        raw = (directory / name).read_bytes()
        if len(raw) != source["bytes"] or sha(raw) != source["sha256"]:
            raise ProfileError("qwen35_resource_checksum_mismatch")
        files[name] = raw
    return manifest, files


def check_show(show, manifest):
    info = show.get("model_info", {})
    header = show.get("modelfile", "").split("LICENSE", 1)[0]
    if (show.get("template") != "{{ .Prompt }}"
            or sha(show["template"].encode()) != manifest["template_sha256"]
            or info.get("general.architecture") != "qwen35"
            or info.get("tokenizer.ggml.pre") != "qwen35"
            or info.get("tokenizer.ggml.model") != "gpt2"
            or info.get("tokenizer.ggml.add_bos_token") is not False
            or info.get("tokenizer.ggml.eos_token_id") != 248046
            or info.get("tokenizer.ggml.padding_token_id") != 248044
            or show.get("details", {}).get("runner") != "llamacpp"
            or not re.search(r"^RENDERER\s+qwen3\.5\s*$", header, re.M)
            or not re.search(r"^PARSER\s+qwen3\.5\s*$", header, re.M)):
        raise ProfileError("qwen35_model_metadata_mismatch")


def validate_exports(directory, show, tags, version):
    manifest, files = load_files(directory)
    if version.strip() != "ollama version is 0.40.5":
        raise ProfileError("qwen35_ollama_version_mismatch")
    check_show(show, manifest)
    matches = [m for m in tags.get("models", []) if m.get("name") == manifest["model"] or m.get("model") == manifest["model"]]
    if len(matches) != 1 or matches[0].get("digest") != manifest["model_digest"]:
        raise ProfileError("qwen35_model_digest_mismatch")
    info = show["model_info"]
    metadata = {k: v for k, v in info.items() if k.startswith("tokenizer.")}
    canonical = json.dumps(metadata, sort_keys=True, ensure_ascii=True, separators=(",", ":")).encode()
    if sha(canonical) != manifest["tokenizer_metadata_sha256"]:
        raise ProfileError("qwen35_exported_tokenizer_mismatch")
    tokens, types, merges = (info["tokenizer.ggml." + k] for k in ("tokens", "token_type", "merges"))
    tokenizer = json.loads(files["tokenizer.json"])
    config = json.loads(files["tokenizer_config.json"])
    model = tokenizer["model"]
    if (model.get("type") != "BPE" or len(tokens) != 248320 or len(merges) != 247587
            or len(types) != len(tokens) or len(model["vocab"]) != 248044):
        raise ProfileError("qwen35_tokenizer_structure_mismatch")
    for text, index in model["vocab"].items():
        if tokens[index] != text:
            raise ProfileError("qwen35_vocabulary_mismatch")
    for token in tokenizer["added_tokens"]:
        index = token["id"]
        configured = config["added_tokens_decoder"][str(index)]
        if (tokens[index] != token["content"] or types[index] not in (3, 4)
                or any(configured.get(k) != token.get(k) for k in ("content", "special", "normalized", "lstrip", "rstrip", "single_word"))):
            raise ProfileError("qwen35_special_token_mismatch")
    local_merges = [" ".join(v) if isinstance(v, list) else v for v in model["merges"]]
    if local_merges != merges or config.get("add_bos_token") is not False or config.get("eos_token") != tokens[248046] or config.get("pad_token") != tokens[248044]:
        raise ProfileError("qwen35_merges_or_special_ids_mismatch")
    return manifest


def render(system, user):
    # Restricted form of Qwen35Renderer.Render at the pinned Ollama commit:
    # system+user, no media/tools, explicit think=false, LeadingBOS="".
    # Callers use the fixed system and canonical JSON (no outside whitespace).
    if system != system.strip() or user != user.strip():
        raise ProfileError("qwen35_unsupported_outer_whitespace")
    return ("<|im_start|>system\n" + system + "<|im_end|>\n"
            + "<|im_start|>user\n" + user + "<|im_end|>\n"
            + "<|im_start|>assistant\n<think>\n\n</think>\n\n")
