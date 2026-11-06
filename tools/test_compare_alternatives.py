import argparse
import datetime as dt
import importlib.util
import json
from pathlib import Path
import tempfile
import sys
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("alternatives", Path(__file__).with_name("compare_alternatives.py"))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

class Tokens:
    def __init__(self,n):self.n=n
    def encode(self,text,add_special_tokens=False):return argparse.Namespace(ids=list(range(self.n)))

class TransportTests(unittest.TestCase):
    def test_two_probes_then_ten_without_repeat(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); manifest=root/"manifest.json";baseline=root/"baseline.jsonl"
            baseline.write_text("baseline",encoding="utf8")
            manifests={k:dict(model=k,digest=k,think=t,reserve=r) for k,t,r in [("gpt-oss","low",4096),("gemma4",False,1000)]}
            manifest.write_text(json.dumps(manifests),encoding="utf8")
            cases=[dict(case_id=f"syn-{i:02d}",user_sha256=str(i),input={}) for i in range(1,7)]
            contract=dict(prompt_sha256="system",schema_sha256="schema",cases=cases)
            calls=[]
            def evaluate(args,key,model,contract,case,tokenizer):
                calls.append((key,case["case_id"]))
                return dict(case_id=case["case_id"],model=model["model"],model_digest=model["digest"],prompt_sha256="system",schema_sha256="schema",user_sha256=case["user_sha256"],done=True,done_reason="stop",generation_attempts=1,error=None,validation_error=None,final_content="{}",runtime_token_count_verified=True,gpu_after=dict(fully_gpu=True),baseline_report_sha256=args.baseline_hash,manifest_sha256=args.manifest_hash),False
            argv=["diagnostic","--bridge",str(root/"bridge"),"--input-dir",directory,"--baseline",str(baseline),"--out-dir",str(root/"reports"),"--run"]
            def bridge(binary,request):return {"error":None} if request["Action"]=="validate" else contract
            with patch.object(m,"MANIFEST",manifest),patch.object(m,"assets",return_value=Tokens(100)),patch.object(m,"bridge",side_effect=bridge),patch.object(m,"evaluate",side_effect=evaluate),patch.object(m,"isolate"),patch.object(m,"unload"),patch.object(m.sys,"prefix","test-venv"),patch.object(m.importlib.metadata,"version",return_value="0.22.1"):
                with patch.object(sys,"argv",argv):self.assertEqual(m.main(),0)
                self.assertEqual(calls,[("gpt-oss","syn-02"),("gemma4","syn-02")])
                # Simulate normal process exit between explicit phases.
                (root/"reports/.generation.lock").unlink()
                with patch.object(sys,"argv",argv+["--stage","remaining"]):self.assertEqual(m.main(),0)
                self.assertEqual(len(calls),12)
                self.assertEqual(len(set(calls)),12)
                (root/"reports/.generation.lock").unlink()
                with patch.object(sys,"argv",argv+["--stage","remaining"]):
                    with self.assertRaisesRegex(ValueError,"both_successful_gpu_probes_required"):m.main()
                self.assertEqual(len(calls),12)

    def test_completion_budget_and_private_reasoning(self):
        for key,think,reserve in [("gpt-oss","low",4096),("gemma4",False,1000)]:
            for scenario in ["ok","length","invalid","count","renderer","overflow"]:
                with self.subTest(key=key,scenario=scenario),tempfile.TemporaryDirectory() as directory:
                    args=argparse.Namespace(base_url="http://mock",bridge="unused",out_dir=Path(directory),baseline_hash="b",manifest_hash="m")
                    manifest=dict(model=key,digest="pin",think=think,reserve=reserve)
                    contract=dict(prompt="system",prompt_version="v2",schema_version="summary-v1",prompt_sha256="s",schema_sha256="f",schema={})
                    case=dict(case_id="syn-02",user="user",input={},input_file_sha256="i",user_sha256="u",qwen_baseline={})
                    generations=[]
                    def call(base,path,payload):
                        if payload.get("_debug_render_only"):
                            rendered=m.render(key,"system","user",dt.datetime.now(dt.timezone.utc).date().isoformat())
                            return dict(created_at=dt.datetime.now(dt.timezone.utc).isoformat(),_debug_info=dict(rendered_template="wrong" if scenario=="renderer" else rendered))
                        generations.append(payload)
                        return dict(done=True,done_reason="length" if scenario=="length" else "stop",prompt_eval_count=101 if scenario=="count" else 100,eval_count=20,
                                    message=dict(content='{"summary":"mock","source_message_ids":["id"]}',thinking="PRIVATE_THINKING"))
                    with patch.object(m,"verify_show",return_value="defaults"),patch.object(m,"gpu",return_value=dict(fully_gpu=True)),patch.object(m,"call",side_effect=call),patch.object(m,"bridge",return_value=dict(error="invalid_summary" if scenario=="invalid" else None)):
                        row,fatal=m.evaluate(args,key,manifest,contract,case,Tokens(9000 if scenario=="overflow" else 100))
                    self.assertNotIn("PRIVATE_THINKING",json.dumps(row))
                    self.assertEqual(bool(row["error"]),scenario!="ok")
                    self.assertEqual(len(generations),0 if scenario in ("overflow","renderer") else 1)
                    if generations:
                        self.assertEqual(generations[0]["think"],think)
                        self.assertEqual(generations[0]["options"]["num_ctx"],8192)
                        self.assertEqual(generations[0]["options"]["num_predict"],reserve)
                    if scenario in ("count","renderer","overflow"):self.assertTrue(fatal)

if __name__=="__main__":unittest.main()
