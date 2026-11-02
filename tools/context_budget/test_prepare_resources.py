import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

from prepare_resources import ResourceError, sha256, validate_bundle


class PrepareTests(unittest.TestCase):
    def fixture(self, directory, corrupt=False, mismatch=False):
        tokenizer = {"model": {"type": "BPE", "vocab": {"a": 0, "b": 1},
                                "merges": ["a b"]}, "added_tokens": []}
        files = {"tokenizer.json": json.dumps(tokenizer).encode(),
                 "tokenizer_config.json": b"{}"}
        manifest = {"repository": "Qwen/Qwen3-8B", "revision": "a" * 40,
                    "model_digest": "b" * 64,
                    "ollama_template_sha256": sha256(b"template"),
                    "files": {name: sha256(data) for name, data in files.items()}}
        files["manifest.json"] = json.dumps(manifest).encode()
        if corrupt:
            files["tokenizer.json"] += b" "
        archive = directory / "bundle.tar.gz"
        with tarfile.open(archive, "w:gz") as bundle:
            for name, data in files.items():
                member = tarfile.TarInfo("qwen3-8b/" + name)
                member.size = len(data)
                bundle.addfile(member, io.BytesIO(data))
        show = {"template": "different" if mismatch else "template", "model_info": {
            "general.architecture": "qwen3", "tokenizer.ggml.model": "gpt2",
            "tokenizer.ggml.pre": "qwen2", "tokenizer.ggml.add_bos_token": False,
            "tokenizer.ggml.tokens": ["a", "b"], "tokenizer.ggml.token_type": [1, 1],
            "tokenizer.ggml.merges": ["a b"]}}
        show_path = directory / "show.json"
        show_path.write_text(json.dumps(show), encoding="utf-8")
        return archive, show_path

    def test_valid(self):
        with tempfile.TemporaryDirectory() as directory:
            files, summary = validate_bundle(*self.fixture(Path(directory)))
            self.assertEqual(set(files), {"manifest.json", "tokenizer.json", "tokenizer_config.json"})
            self.assertFalse(summary["runtime_compatibility_verified"])

    def test_corrupt_checksum(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ResourceError, "tokenizer_checksum_mismatch"):
                validate_bundle(*self.fixture(Path(directory), corrupt=True))

    def test_template_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ResourceError, "template_checksum_mismatch"):
                validate_bundle(*self.fixture(Path(directory), mismatch=True))

    def test_vocabulary_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            archive, show_path = self.fixture(Path(directory))
            show = json.loads(show_path.read_text(encoding="utf-8"))
            show["model_info"]["tokenizer.ggml.tokens"][0] = "different"
            show_path.write_text(json.dumps(show), encoding="utf-8")
            with self.assertRaisesRegex(ResourceError, "vocabulary_mismatch"):
                validate_bundle(archive, show_path)

    def test_merges_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            archive, show_path = self.fixture(Path(directory))
            show = json.loads(show_path.read_text(encoding="utf-8"))
            show["model_info"]["tokenizer.ggml.merges"] = ["b a"]
            show_path.write_text(json.dumps(show), encoding="utf-8")
            with self.assertRaisesRegex(ResourceError, "merges_mismatch"):
                validate_bundle(archive, show_path)


if __name__ == "__main__":
    unittest.main()
