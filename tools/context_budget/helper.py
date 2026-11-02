"""Adapted from the supplied context_budget.py; local tokenizers, no weights/downloads.

count is strictly offline. verify explicitly checks the actual Ollama render-only
extension, never falls back to generation. Thinking is an explicit diagnostic flag;
the default system+user/think=false behavior remains unchanged.
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
DIAGNOSTIC_PROFILE = "qwen3-14b-q4-k-m-v1"
DIAGNOSTIC_MANIFEST_SHA256 = "dc5b73c3845a1fdf043f3055a98b3b0399831dc27592d8f995db034f929489be"


class BudgetError(Exception):
    pass


def digest(data):
    return hashlib.sha256(data).hexdigest()


class ContextBudget:
    def __init__(self, directory, thinking=False, diagnostic_profile=None):
        self.directory = Path(directory)
        self.manifest = None
        self.tokenizer = None
        self.thinking = thinking
        self.output_reserve = 4096 if thinking else 1500
        self.diagnostic_profile = diagnostic_profile

    def load(self):
        if self.tokenizer is not None:
            return
        if sys.prefix == sys.base_prefix:
            raise BudgetError("budget_python_requires_venv")
        if importlib.metadata.version("tokenizers") != TOKENIZERS_VERSION:
            raise BudgetError("tokenizers_version_mismatch")
        manifest_raw = (self.directory / "manifest.json").read_bytes()
        manifest = json.loads(manifest_raw)
        if self.diagnostic_profile is not None:
            if (self.diagnostic_profile != DIAGNOSTIC_PROFILE or self.thinking
                    or manifest.get("diagnostic_profile") != DIAGNOSTIC_PROFILE
                    or digest(manifest_raw) != DIAGNOSTIC_MANIFEST_SHA256):
                raise BudgetError("invalid_tokenizer_manifest")
        elif manifest.get("diagnostic_profile") is not None:
            raise BudgetError("invalid_tokenizer_manifest")
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
    def render(system, user, thinking=False):
        # Same two branches as the supplied context_budget.py, without changing data.
        result = f"<|im_start|>system\n\n{system}<|im_end|>\n"
        result += f'<|im_start|>user\n{user} /{"think" if thinking else "no_think"}<|im_end|>\n'
        result += "<|im_start|>assistant\n"
        if not thinking:
            result += "<think>\n\n</think>\n\n"
        return result

    def count(self, system, user):
        self.load()
        count = len(self.tokenizer.encode(self.render(system, user, self.thinking), add_special_tokens=False).ids)
        return {"input_tokens": count, "output_reserve": self.output_reserve, "context_tokens": 8192,
                "required_tokens": count + self.output_reserve, "remaining_tokens": 8192 - count - self.output_reserve,
                "method": METHOD, "compatibility_verified": False}

    def verify(self, base_url, model, system, user, http_timeout=5, output_schema=None):
        self.load()
        if self.diagnostic_profile and model != self.manifest["model_name"]:
            raise BudgetError("context_tokenizer_model_not_validated")
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
        payload = {
            "model": model, "stream": False, "think": self.thinking, "truncate": False, "shift": False,
            "_debug_render_only": True, "keep_alive": "30m",
            "options": {"num_ctx": 8192, "num_predict": self.output_reserve, "temperature": 0},
            "messages": [{"role": "system", "content": system}, {"role": "user", "content": user}],
        }
        if self.thinking:
            if not isinstance(output_schema, dict) or not output_schema:
                raise BudgetError("budget_helper_invalid_request")
            payload["format"] = output_schema
            payload["options"].update(temperature=0.6, top_p=0.95, top_k=20, min_p=0)
        response = call("/api/chat", payload)
        # Do not mistake ignored _debug_render_only + ordinary generation for a pass.
        info = response.get("_debug_info")
        if not isinstance(info, dict) or not isinstance(info.get("rendered_template"), str):
            raise BudgetError("context_render_only_unavailable")
        if info["rendered_template"] != self.render(system, user, self.thinking):
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
    parser.add_argument("--thinking", action="store_true", help="diagnostic only: think=true, reserve=4096")
    parser.add_argument("--diagnostic-profile", choices=(DIAGNOSTIC_PROFILE,))
    args = parser.parse_args()
    try:
        if not 0 < args.http_timeout <= 120:
            raise BudgetError("budget_helper_invalid_timeout")
        raw = sys.stdin.buffer.read(MAX_INPUT + 1)
        if len(raw) > MAX_INPUT:
            raise BudgetError("budget_helper_input_too_large")
        request = json.loads(raw)
        expected = {"system", "user"} if args.mode == "count" else {"system", "user", "base_url", "model"}
        if args.thinking and args.mode == "verify":
            expected.add("format")
        if (not isinstance(request, dict) or set(request) != expected
                or any(not isinstance(v, str) for k, v in request.items() if k != "format")
                or ("format" in expected and not isinstance(request["format"], dict))):
            raise BudgetError("budget_helper_invalid_request")
        counter = ContextBudget(args.resources, thinking=args.thinking, diagnostic_profile=args.diagnostic_profile)
        if args.mode == "count":
            result = counter.count(request["system"], request["user"])
        else:
            result = counter.verify(request["base_url"], request["model"], request["system"], request["user"], args.http_timeout, request.get("format"))
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
