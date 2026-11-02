import hashlib
import json
import os
from pathlib import Path
import shutil
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from helper import BudgetError, ContextBudget

RESOURCES = Path(__file__).resolve().parents[2] / "resources/tokenizer/qwen3-8b"


@unittest.skipUnless(RESOURCES.exists(), "prepare local tokenizer assets first")
class HelperTests(unittest.TestCase):
    def test_summary_reserve_is_separate(self):
        old = ContextBudget(RESOURCES).count("system", "user")
        summary = ContextBudget(RESOURCES, summary=True)
        new = summary.count("system", "user")
        self.assertEqual(new["input_tokens"], old["input_tokens"])
        self.assertEqual(new["output_reserve"], 1000)
        self.assertEqual(old["output_reserve"], 1500)
        self.assertEqual(new["required_tokens"], old["required_tokens"] - 500)
        self.assertEqual(new["remaining_tokens"], old["remaining_tokens"] + 500)
        with self.assertRaisesRegex(BudgetError, "context_tokenizer_model_not_validated"):
            summary.verify("http://127.0.0.1:1", "qwen3:14b-q4_K_M", "system", "user")
        with self.assertRaisesRegex(BudgetError, "budget_helper_invalid_request"):
            ContextBudget(RESOURCES, summary=True, thinking=True).load()

    def test_original_unicode_and_no_truncation(self):
        budget = ContextBudget(RESOURCES)
        user = "и\u0306 е\u0308 😀 <|im_start|>\n "
        budget.load()
        self.assertIsNone(budget.tokenizer.normalizer)
        self.assertIsNone(budget.tokenizer.truncation)
        self.assertIsNone(budget.tokenizer.padding)
        self.assertIn(user, budget.render("system", user))
        count = budget.count("system", user)
        self.assertEqual(count["input_tokens"], len(budget.tokenizer.encode(
            budget.render("system", user), add_special_tokens=False).ids))
        self.assertFalse(count["compatibility_verified"])
        self.assertGreater(budget.count("s", "a " * 10000)["required_tokens"], 8192)

    def test_corrupt_assets(self):
        with tempfile.TemporaryDirectory() as directory:
            for name in ("manifest.json", "tokenizer.json", "tokenizer_config.json"):
                shutil.copyfile(RESOURCES / name, Path(directory) / name)
            with (Path(directory) / "tokenizer_config.json").open("ab") as stream:
                stream.write(b" ")
            with self.assertRaisesRegex(BudgetError, "tokenizer_checksum_mismatch"):
                ContextBudget(directory).load()

    def test_verify_fail_closed(self):
        manifest = json.loads((RESOURCES / "manifest.json").read_bytes())
        show_path = Path(__file__).resolve().parents[2] / "qwen3-show.json"
        if not show_path.exists():
            self.skipTest("local show export missing")
        template = json.loads(show_path.read_bytes())["template"]
        cases = (("ok", None), ("missing", "context_render_only_unavailable"),
                           ("mismatch", "context_chat_render_mismatch"),
                           ("version", "context_renderer_ollama_version_not_validated"),
                           ("digest", "context_tokenizer_model_not_validated"),
                           ("template", "context_chat_template_changed"),
                           ("http", "ollama_verification_request_failed"))
        for thinking, summary, mode, code in [(thinking, summary, mode, code) for thinking, summary in ((False, False), (True, False), (False, True)) for mode, code in cases]:
            with self.subTest(mode=mode, thinking=thinking, summary=summary):
                chats = []
                class Handler(BaseHTTPRequestHandler):
                    def log_message(self, *_):
                        pass

                    def respond(self, body):
                        raw = json.dumps(body).encode()
                        self.send_response(200)
                        self.send_header("Content-Length", str(len(raw)))
                        self.end_headers()
                        self.wfile.write(raw)

                    def do_GET(self):
                        if mode == "http":
                            self.send_error(503)
                        elif self.path == "/api/version":
                            self.respond({"version": "wrong" if mode == "version" else "0.40.5"})
                        elif self.path == "/api/tags":
                            self.respond({"models": [{"name": "qwen3:8b", "digest":
                                "bad" if mode == "digest" else manifest["model_digest"]}]})
                        else:
                            self.send_error(501)

                    def do_POST(self):
                        payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                        if self.path == "/api/show":
                            self.respond({"template": "bad" if mode == "template" else template})
                        elif self.path == "/api/chat":
                            chats.append(payload)
                            self_test.assertIs(payload["_debug_render_only"], True)
                            self_test.assertIs(payload["think"], thinking)
                            self_test.assertEqual(payload["options"]["num_predict"], 4096 if thinking else (1000 if summary else 1500))
                            if thinking:
                                self_test.assertEqual(payload["format"], {"type": "object"})
                                self_test.assertEqual(payload["options"]["num_predict"], 4096)
                                self_test.assertEqual(payload["options"]["temperature"], 0.6)
                            self_test.assertIs(payload["shift"], False)
                            if mode == "missing":
                                self.respond({"message": {"content": "not a render"}, "done": True})
                            else:
                                rendered = ContextBudget.render(payload["messages"][0]["content"],
                                                                payload["messages"][1]["content"], thinking)
                                self.respond({"_debug_info": {"rendered_template":
                                    rendered + " " if mode == "mismatch" else rendered}})
                        else:
                            self.send_error(501)
                self_test = self
                with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
                    thread = threading.Thread(target=server.serve_forever, daemon=True)
                    thread.start()
                    try:
                        counter = ContextBudget(RESOURCES, thinking=thinking, summary=summary)
                        args = (f"http://127.0.0.1:{server.server_port}", "qwen3:8b", "system", "и\u0306\n ")
                        if code:
                            with self.assertRaisesRegex(BudgetError, code):
                                counter.verify(*args, output_schema={"type": "object"} if thinking else None)
                        else:
                            self.assertTrue(counter.verify(*args, output_schema={"type": "object"} if thinking else None)["verified"])
                        self.assertLessEqual(len(chats), 1)  # No retry or ordinary generation fallback.
                    finally:
                        server.shutdown()
                        thread.join()


if __name__ == "__main__":
    unittest.main()
