import json
from pathlib import Path
import unittest

from qwen35_profile import ProfileError, load_files, pin, render, validate_exports

ROOT = Path(__file__).resolve().parents[2]
RESOURCES = ROOT / "resources/tokenizer/qwen35-9b"


class Qwen35RenderTests(unittest.TestCase):
    def test_independent_text_template(self):
        self.assertEqual(render("s", "u"), "<|im_start|>system\ns<|im_end|>\n<|im_start|>user\nu<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n")
        with self.assertRaisesRegex(ProfileError, "unsupported_outer_whitespace"):
            render(" s", "u")
        with self.assertRaisesRegex(ProfileError, "manifest_pin_mismatch"):
            pin(b"{}")


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


if __name__ == "__main__":
    unittest.main()
