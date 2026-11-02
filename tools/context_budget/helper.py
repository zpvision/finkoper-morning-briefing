"""Adapted from the supplied context_budget.py; local tokenizers, no weights/downloads.

count is strictly offline. verify explicitly checks the actual Ollama render-only
extension, never falls back to generation, and supports only system+user/think=false.
"""
import argparse
import hashlib
import importlib.metadata
import json
from pathlib import Path
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

from tokenizers import Tokenizer

OLLAMA_VERSION = "0.40.5"
TOKENIZERS_VERSION = "0.22.1"
MAX_INPUT = 2 * 1024 * 1024
MAX_RESPONSE = 8 * 1024 * 1024
METHOD = "qwen3_tokenizers_ollama_template_no_normalization"


class BudgetError(Exception):
    pass


def digest(data):
    return hashlib.sha256(data).hexdigest()


class ContextBudget:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.manifest = None
        self.tokenizer = None

    def load(self):
        if self.tokenizer is not None:
            return
        if sys.prefix == sys.base_prefix:
            raise BudgetError("budget_python_requires_venv")
        if importlib.metadata.version("tokenizers") != TOKENIZERS_VERSION:
            raise BudgetError("tokenizers_version_mismatch")
        manifest = json.loads((self.directory / "manifest.json").read_bytes())
        if (manifest.get("repository") != "Qwen/Qwen3-8B"
                or not re.fullmatch(r"[a-f0-9]{40}", manifest.get("revision", ""))
                or not re.fullmatch(r"[a-f0-9]{64}", manifest.get("model_digest", ""))
                or not re.fullmatch(r"[a-f0-9]{64}", manifest.get("ollama_template_sha256", ""))):
            raise BudgetError("invalid_tokenizer_manifest")
        files = {}
        for name in ("tokenizer.json", "tokenizer_config.json"):
            files[name] = (self.directory / name).read_bytes()
            if digest(files[name]) != manifest["files"][name]:
                raise BudgetError("tokenizer_checksum_mismatch")
        # Original solution: never normalize generation text to match HF assets.
        tokenizer = Tokenizer.from_str(files["tokenizer.json"].decode("utf-8"))
        tokenizer.normalizer = None
        tokenizer.no_truncation()
        tokenizer.no_padding()
        self.manifest, self.tokenizer = manifest, tokenizer

    @staticmethod
    def render(system, user):
        return (f"<|im_start|>system\n\n{system}<|im_end|>\n"
                f"<|im_start|>user\n{user} /no_think<|im_end|>\n"
                "<|im_start|>assistant\n<think>\n\n</think>\n\n")

    def count(self, system, user):
        self.load()
        count = len(self.tokenizer.encode(self.render(system, user), add_special_tokens=False).ids)
        return {"input_tokens": count, "output_reserve": 1500, "context_tokens": 8192,
                "required_tokens": count + 1500, "remaining_tokens": 8192 - count - 1500,
                "method": METHOD, "compatibility_verified": False}

    def verify(self, base_url, model, system, user, http_timeout=5):
        self.load()
        parsed = urllib.parse.urlsplit(base_url)
        if (parsed.scheme not in ("http", "https") or not parsed.netloc
                or parsed.username or parsed.password or parsed.query or parsed.fragment):
            raise BudgetError("invalid_ollama_url")
        # No proxy environment, credentials, cookies or redirects for verification.
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, req, fp, code, msg, headers, newurl):
                return None
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

        def call(path, payload=None):
            data = None if payload is None else json.dumps(payload, ensure_ascii=False).encode("utf-8")
            req = urllib.request.Request(base_url.rstrip("/") + path, data=data,
                                         headers={"Content-Type": "application/json"})
            try:
                with opener.open(req, timeout=http_timeout) as response:
                    raw = response.read(MAX_RESPONSE + 1)
                if len(raw) > MAX_RESPONSE:
                    raise BudgetError("ollama_verification_response_too_large")
                body = json.loads(raw)
                if not isinstance(body, dict) or body.get("error"):
                    raise BudgetError("ollama_verification_invalid_response")
                return body
            except (urllib.error.URLError, OSError, ValueError):
                raise BudgetError("ollama_verification_request_failed") from None

        if call("/api/version").get("version") != OLLAMA_VERSION:
            raise BudgetError("context_renderer_ollama_version_not_validated")

        def check_model():
            tags = call("/api/tags").get("models", [])
            matching = [entry for entry in tags if entry.get("name") == model or entry.get("model") == model]
            if len(matching) != 1 or matching[0].get("digest") != self.manifest["model_digest"]:
                raise BudgetError("context_tokenizer_model_not_validated")
            template = call("/api/show", {"model": model}).get("template")
            if not isinstance(template, str) or digest(template.encode("utf-8")) != self.manifest["ollama_template_sha256"]:
                raise BudgetError("context_chat_template_changed")

        check_model()
        response = call("/api/chat", {
            "model": model, "stream": False, "think": False, "truncate": False, "shift": False,
            "_debug_render_only": True, "keep_alive": "30m",
            "options": {"num_ctx": 8192, "num_predict": 1500, "temperature": 0},
            "messages": [{"role": "system", "content": system}, {"role": "user", "content": user}],
        })
        # Do not mistake ignored _debug_render_only + ordinary generation for a pass.
        info = response.get("_debug_info")
        if not isinstance(info, dict) or not isinstance(info.get("rendered_template"), str):
            raise BudgetError("context_render_only_unavailable")
        if info["rendered_template"] != self.render(system, user):
            raise BudgetError("context_chat_render_mismatch")
        check_model()
        return {"verified": True, "model_digest": self.manifest["model_digest"],
                "ollama_version": OLLAMA_VERSION, "template_sha256": self.manifest["ollama_template_sha256"],
                "method": METHOD}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--resources", required=True)
    parser.add_argument("--mode", choices=("count", "verify"), required=True)
    parser.add_argument("--http-timeout", type=float, default=5)
    args = parser.parse_args()
    try:
        if not 0 < args.http_timeout <= 120:
            raise BudgetError("budget_helper_invalid_timeout")
        raw = sys.stdin.buffer.read(MAX_INPUT + 1)
        if len(raw) > MAX_INPUT:
            raise BudgetError("budget_helper_input_too_large")
        request = json.loads(raw)
        expected = {"system", "user"} if args.mode == "count" else {"system", "user", "base_url", "model"}
        if not isinstance(request, dict) or set(request) != expected or any(not isinstance(v, str) for v in request.values()):
            raise BudgetError("budget_helper_invalid_request")
        counter = ContextBudget(args.resources)
        if args.mode == "count":
            result = counter.count(request["system"], request["user"])
        else:
            result = counter.verify(request["base_url"], request["model"], request["system"], request["user"], args.http_timeout)
        print(json.dumps(result, ensure_ascii=True))
        return 0
    except BudgetError as error:
        print(json.dumps({"error": str(error)}))
    except Exception:
        # Never emit tracebacks, source strings, upstream responses or arbitrary exception text.
        print(json.dumps({"error": "budget_helper_failed"}))
    return 1


if __name__ == "__main__":
    sys.exit(main())
