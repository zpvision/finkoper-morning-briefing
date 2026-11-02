import json
from pathlib import Path
import tempfile
import unittest

from helper import BudgetError, ContextBudget, DIAGNOSTIC_PROFILE
from prepare_14b_profile import prepare
from prepare_resources import ResourceError

ROOT = Path(__file__).resolve().parents[2]
SOURCE = ROOT / "resources/tokenizer/qwen3-8b"
TARGET = ROOT / "resources/tokenizer/qwen3-14b"


@unittest.skipUnless((ROOT / "qwen3-14b-show.json").exists() and TARGET.exists(), "prepare local exported profile first")
class Profile14Tests(unittest.TestCase):
    def test_proof_and_source_unchanged(self):
        before = {name: (SOURCE / name).read_bytes() for name in ("manifest.json", "tokenizer.json", "tokenizer_config.json")}
        files = prepare(SOURCE, ROOT / "qwen3-show.json", ROOT / "qwen3-14b-show.json", ROOT / "ollama-tags.json")
        self.assertEqual(files["manifest.json"], (TARGET / "manifest.json").read_bytes())
        for name, data in before.items():
            self.assertEqual(data, (SOURCE / name).read_bytes())
            if name != "manifest.json":
                self.assertEqual(data, files[name])

    def test_mismatch_rejected(self):
        original = json.loads((ROOT / "qwen3-14b-show.json").read_bytes())
        for key in ("tokenizer.ggml.tokens", "tokenizer.ggml.merges", "tokenizer.ggml.token_type", "tokenizer.ggml.eos_token_id"):
            with self.subTest(key=key), tempfile.TemporaryDirectory() as directory:
                changed = json.loads(json.dumps(original))
                if isinstance(changed["model_info"][key], list):
                    changed["model_info"][key][0] = "changed"
                else:
                    changed["model_info"][key] = 0
                path = Path(directory) / "show.json"
                path.write_text(json.dumps(changed), encoding="utf-8")
                with self.assertRaisesRegex(ResourceError, "exported_tokenizer_metadata_mismatch"):
                    prepare(SOURCE, ROOT / "qwen3-show.json", path, ROOT / "ollama-tags.json")
        with tempfile.TemporaryDirectory() as directory:
            changed = dict(original, template=original["template"] + " ")
            path = Path(directory) / "show.json"
            path.write_text(json.dumps(changed), encoding="utf-8")
            with self.assertRaisesRegex(ResourceError, "template_mismatch"):
                prepare(SOURCE, ROOT / "qwen3-show.json", path, ROOT / "ollama-tags.json")

    def test_helper_explicit_profile_only_and_same_counts(self):
        with self.assertRaisesRegex(BudgetError, "invalid_tokenizer_manifest"):
            ContextBudget(TARGET).load()
        with self.assertRaisesRegex(BudgetError, "invalid_tokenizer_manifest"):
            ContextBudget(TARGET, thinking=True, diagnostic_profile=DIAGNOSTIC_PROFILE).load()
        new = ContextBudget(TARGET, diagnostic_profile=DIAGNOSTIC_PROFILE)
        old = ContextBudget(SOURCE)
        self.assertEqual(new.count("system", "Данные ё и\u0306"), old.count("system", "Данные ё и\u0306"))
        with self.assertRaisesRegex(BudgetError, "context_tokenizer_model_not_validated"):
            new.verify("http://127.0.0.1:1", "qwen3:8b", "system", "user")


if __name__ == "__main__":
    unittest.main()
