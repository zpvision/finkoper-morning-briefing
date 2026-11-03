import json
from pathlib import Path
import unittest
import contextlib
import io
import shutil
import tempfile

from qwen35_profile import ProfileError, load_files, pin, render, validate_exports, mode_config

ROOT = Path(__file__).resolve().parents[2]
RESOURCES = ROOT / "resources/tokenizer/qwen35-9b"


class Qwen35RenderTests(unittest.TestCase):
    def test_independent_text_template(self):
        self.assertEqual(render("s", "u"), "<|im_start|>system\ns<|im_end|>\n<|im_start|>user\nu<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n")
        with self.assertRaisesRegex(ProfileError, "unsupported_outer_whitespace"):
            render(" s", "u")
        with self.assertRaisesRegex(ProfileError, "manifest_pin_mismatch"):
            pin(b"{}")
        self.assertEqual(render("s", "u", True), "<|im_start|>system\ns<|im_end|>\n<|im_start|>user\nu<|im_end|>\n<|im_start|>assistant\n<think>\n")


@unittest.skipUnless(RESOURCES.exists(), "prepare separate Qwen3.5 assets")
class Qwen35AssetTests(unittest.TestCase):
    def test_pinned_assets_and_exports(self):
        manifest, files = load_files(RESOURCES)
        show = json.loads((ROOT / "qwen35-9b-show.json").read_bytes())
        tags = json.loads((ROOT / "ollama-tags.json").read_bytes())
        version = (ROOT / "ollama-version.txt").read_text(encoding="utf-8-sig")
        self.assertEqual(validate_exports(RESOURCES, show, tags, version)["model"], "qwen3.5:9b")
        changed = dict(show, template="{{ .Messages }}")
        with self.assertRaisesRegex(ProfileError, "metadata_mismatch"):
            validate_exports(RESOURCES, changed, tags, version)
        changed_tags = {"models": [dict(tags["models"][0], digest="0" * 64)]}
        with self.assertRaisesRegex(ProfileError, "digest_mismatch"):
            validate_exports(RESOURCES, show, changed_tags, version)

    def test_no_normalization_no_truncation(self):
        from qwen35_helper import count
        from tokenizers import Tokenizer
        _, tokens = count(RESOURCES, "system", '{"text":"и\u0306 е\u0308 😀"}')
        tokenizer = Tokenizer.from_file(str(RESOURCES / "tokenizer.json"))
        tokenizer.normalizer = None
        tokenizer.no_padding()
        tokenizer.no_truncation()
        self.assertEqual(tokens, tokenizer.encode(render("system", '{"text":"и\u0306 е\u0308 😀"}'), add_special_tokens=False).ids)
        _, long_tokens = count(RESOURCES, "system", ("длинный " * 10000).rstrip())
        self.assertGreater(len(long_tokens) + 1000, 8192)

    def test_thinking_tokens_and_sampling_from_pinned_resources(self):
        from qwen35_helper import count
        from tokenizers import Tokenizer
        manifest, files = load_files(RESOURCES)
        self.assertIn("Thinking mode for general tasks: `temperature=1.0, top_p=0.95, top_k=20, min_p=0.0, presence_penalty=1.5, repetition_penalty=1.0`", files["README.md"].decode())
        self.assertIn('sb.WriteString("<think>\\n")', files["ollama__model__renderers__qwen35.go.source"].decode())
        thinking, options = mode_config(manifest, "thinking")
        self.assertTrue(thinking)
        self.assertEqual(options, dict(temperature=1.0, top_p=.95, top_k=20, min_p=0, presence_penalty=1.5, repeat_penalty=1, num_ctx=8192, num_predict=4096))
        tokenizer = Tokenizer.from_str(files["tokenizer.json"].decode())
        tokenizer.normalizer = None
        tokenizer.no_padding()
        tokenizer.no_truncation()
        _, tokens = count(RESOURCES,"system",'{"text":"проверка"}', True)
        self.assertEqual(tokens,tokenizer.encode(render("system",'{"text":"проверка"}',True),add_special_tokens=False).ids)
        _, plain = count(RESOURCES,"system",'{"text":"проверка"}', False)
        self.assertNotEqual(tokens,plain)
        long_thinking, long_options = mode_config(manifest, "thinking-long")
        self.assertTrue(long_thinking)
        options["num_predict"] = 6144
        self.assertEqual(long_options, options)

    def test_read_only_resource_verifier(self):
        from verify_qwen35 import main
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            code = main(["--resources",str(RESOURCES),"--show",str(ROOT/"qwen35-9b-show.json"),"--tags",str(ROOT/"ollama-tags.json"),"--version",str(ROOT/"ollama-version.txt")])
        self.assertEqual(code,0)
        result = json.loads(output.getvalue())
        self.assertTrue(result["offline_assets_verified"])
        self.assertTrue(result["exports_verified"])
        self.assertFalse(result["runtime_token_count_verified"])
        with tempfile.TemporaryDirectory() as directory:
            copied = Path(directory)/"resources"
            shutil.copytree(RESOURCES,copied)
            changed = copied/"README.md"
            changed.write_bytes(changed.read_bytes()+b"changed")
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                code = main(["--resources",str(copied)])
            self.assertEqual(code,1)
            self.assertEqual(json.loads(output.getvalue())["error"],"qwen35_resource_checksum_mismatch")
            self.assertTrue(changed.read_bytes().endswith(b"changed"))  # No silent repair.


if __name__ == "__main__":
    unittest.main()
