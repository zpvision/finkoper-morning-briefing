"""Isolated Qwen3.5 CPU counter. No weights, GPU or downloads. No 8B resources."""
import argparse
import importlib.metadata
import json
from pathlib import Path
import sys
import urllib.parse
import urllib.request

from tokenizers import Tokenizer
sys.path.insert(0, str(Path(__file__).resolve().parent))  # Trusted sibling module; -I excludes cwd/PYTHONPATH.
from qwen35_profile import PROFILE, PROFILE27, ProfileError, METHOD, check_show, load_files, render, mode_config

MAX_RESPONSE = 32 * 1024 * 1024


def count(directory, system, user, thinking=False, profile=PROFILE):
    manifest, files = load_files(directory, profile)
    if sys.prefix == sys.base_prefix or importlib.metadata.version("tokenizers") != "0.22.1":
        raise ProfileError("qwen35_requires_tokenizers_0_22_1_venv")
    tokenizer = Tokenizer.from_str(files["tokenizer.json"].decode())
    tokenizer.normalizer = None  # GGUF BPE path does not run HF's NFC. Never modify generation input.
    tokenizer.no_padding()
    tokenizer.no_truncation()
    tokens = tokenizer.encode(render(system, user, thinking), add_special_tokens=False).ids
    return manifest, tokens


def verify(manifest, base, model, system, user, timeout, mode="baseline"):
    if model != manifest["model"]:
        raise ProfileError("qwen35_model_digest_mismatch")
    parsed = urllib.parse.urlsplit(base)
    if parsed.scheme not in ("http", "https") or not parsed.netloc or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ProfileError("qwen35_invalid_base_url")
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    def call(path, payload=None):
        raw = None if payload is None else json.dumps(payload, ensure_ascii=False).encode()
        req = urllib.request.Request(base.rstrip("/") + path, data=raw, headers={"Content-Type": "application/json"})
        try:
            with opener.open(req, timeout=timeout) as response:
                raw = response.read(MAX_RESPONSE + 1)
            if len(raw) > MAX_RESPONSE:
                raise ValueError()
            result = json.loads(raw)
            if not isinstance(result, dict) or result.get("error"):
                raise ValueError()
            return result
        except Exception as error:
            # A fixed class name is safe diagnostics; never include URLs, bodies or exception text.
            code = type(error).__name__
            if code not in {"HTTPError", "URLError", "TimeoutError", "ConnectionResetError", "IncompleteRead", "JSONDecodeError", "UnicodeDecodeError", "ValueError"}:
                code = "other"
            raise ProfileError("qwen35_verification_request_failed_" + code) from None
    if call("/api/version").get("version") != manifest["ollama_version"]:
        raise ProfileError("qwen35_ollama_version_mismatch")
    def metadata():
        matches = [m for m in call("/api/tags").get("models", []) if m.get("name") == model or m.get("model") == model]
        if len(matches) != 1 or matches[0].get("digest") != manifest["model_digest"]:
            raise ProfileError("qwen35_model_digest_mismatch")
        check_show(call("/api/show", {"model": model}), manifest)
    metadata()
    thinking, options = mode_config(manifest, mode)
    result = call("/api/chat", {"model": model, "think": thinking, "stream": False, "keep_alive": "30m", "truncate": False, "shift": False,
        "_debug_render_only": True, "options": options, "messages": [{"role": "system", "content": system}, {"role": "user", "content": user}]})
    info = result.get("_debug_info")
    actual = info.get("rendered_template") if isinstance(info, dict) else None
    if not isinstance(actual, str):
        raise ProfileError("qwen35_render_only_missing_rendered_template")
    if actual != render(system, user, thinking):
        raise ProfileError("qwen35_runtime_renderer_mismatch")
    if info.get("image_count", 0) != 0:
        raise ProfileError("qwen35_unexpected_media")
    metadata()
    return {"verified": True, "model_digest": manifest["model_digest"], "ollama_version": manifest["ollama_version"], "template_sha256": manifest["template_sha256"], "method": METHOD}


def main(profile=PROFILE):
    parser = argparse.ArgumentParser()
    parser.add_argument("--resources", required=True)
    parser.add_argument("--mode", choices=("count", "verify"), required=True)
    parser.add_argument("--http-timeout", type=float, default=30)
    parser.add_argument("--qwen35-mode", choices=("baseline", "greedy", "thinking", "thinking-long"), default="baseline")
    args = parser.parse_args()
    try:
        if profile == PROFILE27 and args.qwen35_mode != "baseline":
            raise ProfileError("qwen35_helper_failed")
        if not 0 < args.http_timeout <= 120:
            raise ProfileError("qwen35_invalid_timeout")
        raw = sys.stdin.buffer.read(2 * 1024 * 1024 + 1)
        if len(raw) > 2 * 1024 * 1024:
            raise ProfileError("qwen35_input_size")
        request = json.loads(raw)
        keys = {"system", "user"} if args.mode == "count" else {"system", "user", "base_url", "model"}
        if not isinstance(request, dict) or set(request) != keys or any(not isinstance(v, str) for v in request.values()):
            raise ProfileError("qwen35_invalid_request")
        manifest, tokens = count(args.resources, request["system"], request["user"], args.qwen35_mode in ("thinking", "thinking-long"), profile)
        _, options = mode_config(manifest, args.qwen35_mode)
        reserve = options["num_predict"]
        if args.mode == "count":
            result = {"input_tokens": len(tokens), "output_reserve": reserve, "context_tokens": 8192,
                "required_tokens": len(tokens) + reserve, "remaining_tokens": 8192 - reserve - len(tokens), "method": METHOD, "compatibility_verified": False}
        else:
            result = verify(manifest, request["base_url"], request["model"], request["system"], request["user"], args.http_timeout, args.qwen35_mode)
        print(json.dumps(result))
        return 0
    except ProfileError as error:
        print(json.dumps({"error": str(error)}))
    except Exception:
        print(json.dumps({"error": "qwen35_helper_failed"}))
    return 1


if __name__ == "__main__":
    sys.exit(main())
