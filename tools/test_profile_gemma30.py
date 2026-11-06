import argparse
import json
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest.mock import patch
from tools import compare_alternatives as client
from tools import profile_gemma30 as profile


class Tokenizer:
    def encode(self,*args,**kwargs):return argparse.Namespace(ids=[1]*100)


def contract():
    return dict(prompt="system",prompt_sha256="prompt",schema_sha256="schema",schema={},cases=[
        dict(case_id=f"long-{i:02d}",user=f"user{i}",user_sha256=str(i),input_file_sha256=str(i),
             input=dict(context_messages=[],messages=[dict(id="id",text="ORIGINAL",sent_at="date",sender_role="client",channel="email")])) for i in range(1,31)])


class ProfilingTests(unittest.TestCase):
    def test_cached_metadata_retains_every_input_guard(self):
        for scenario in ("ok","renderer","budget","count","invalid","length","digest"):
            with self.subTest(scenario=scenario),tempfile.TemporaryDirectory() as name:
                args=argparse.Namespace(out_dir=Path(name),base_url="http://mock",bridge="helper",baseline_hash=None,manifest_hash="pin",suite="gemma30",
                                        profile_optimized=True,profile_strict=True,profile_verifier=lambda *args:"cached defaults")
                cases=contract();cases.update(prompt_version="diagnostic-summary-v2",schema_version="summary-v1")
                case=cases["cases"][0];calls=[]
                def call(base,path,payload):
                    if payload.get("_debug_render_only"):
                        return dict(_debug_info=dict(rendered_template="wrong" if scenario=="renderer" else client.render("gemma4","system",case["user"],"")))
                    calls.append(payload)
                    return dict(done=True,done_reason="length" if scenario=="length" else "stop",prompt_eval_count=101 if scenario=="count" else 100,
                                message=dict(content='{"summary":"fixture","source_message_ids":["id"]}',thinking="PRIVATE_REASONING"))
                tokenizer=Tokenizer()
                if scenario=="budget":tokenizer.encode=lambda *args,**kwargs:argparse.Namespace(ids=[1]*8000)
                with patch.object(client,"call",side_effect=call),patch.object(client,"verify_show",side_effect=AssertionError("no uncached metadata inside case")),patch.object(client,"gpu",return_value=dict(fully_gpu=True,digest="other" if scenario=="digest" else "pin")),patch.object(client,"bridge",return_value=dict(error="invalid sources" if scenario=="invalid" else None)):
                    row,fatal=client.evaluate(args,"gemma4",dict(model="gemma4:26b",digest="pin",think=False,reserve=1000),cases,case,tokenizer)
                self.assertEqual(bool(row["error"]),scenario!="ok")
                self.assertEqual(len(calls),0 if scenario in ("renderer","budget","digest") else 1)
                self.assertNotIn("PRIVATE_REASONING",json.dumps(row))
                if scenario in ("renderer","budget","count","digest"):self.assertTrue(fatal)

    def test_conditional_concurrency_and_matching_plan(self):
        for fast in (False,True):
            with self.subTest(fast=fast),tempfile.TemporaryDirectory() as name:
                root=Path(name);cases=contract();manifest=root/"manifest";manifest.write_text("{}")
                plan=dict(manifest_sha256="m",prompt_sha256="prompt",schema_sha256="schema",profiling=dict(threshold_per_hour=1500),
                          cases=[dict(case_id=c["case_id"],user_sha256=c["user_sha256"],input_file_sha256=c["input_file_sha256"],budget_ok=True) for c in cases["cases"]])
                (root/"plan.json").write_text(json.dumps(plan))
                args=argparse.Namespace(out_dir=root,manifest_hash="m",contract_telemetry={})
                modes=[]
                def run(client,args,pin,cases,mode):
                    modes.append(mode);(root/mode).mkdir()
                    return dict(mode=mode,successes=30,error=None,case_errors=[],full_ms=60000 if fast else 100000)
                with patch.object(profile,"run_mode",side_effect=run),patch.object(profile,"write_profile"):
                    self.assertEqual(profile.run_comparison(client,args,{},cases,manifest),0)
                    self.assertEqual(modes,["current","optimized"]+([] if fast else ["concurrent2"]))
                    with self.assertRaisesRegex(ValueError,"profile_outputs_exist_no_repeat"):
                        profile.run_comparison(client,args,{},cases,manifest)

    def test_bounded_parallelism_no_repeats_and_fatal_drain(self):
        for fatal in (False,True):
            with self.subTest(fatal=fatal),tempfile.TemporaryDirectory() as name:
                root=Path(name);resource=root/"resources/tokenizer/gemma4";resource.mkdir(parents=True)
                raw=b"{}";(resource/"tokenizer.json").write_bytes(raw)
                pin=dict(model="gemma4:26b",digest="pin",ollama_version="0.40.15",sources={"tokenizer.json":dict(bytes=2,sha256=client.sha(raw))})
                args=argparse.Namespace(out_dir=root,base_url="http://mock",suite="gemma30")
                calls=[];active=[0];maximum=[0];lock=threading.Lock()
                def assets(key,pin,telemetry):telemetry["tokenizer_engine_loads"]=1;return Tokenizer()
                def verify(base,pin,metrics,local_tokenizer=None):metrics["metadata_full_checks"]=metrics.get("metadata_full_checks",0)+1;return "defaults"
                def call(base,path,payload=None):
                    if payload:return dict(_debug_info=dict(rendered_template=client.render("gemma4","system","user1","")))
                    return dict(models=[dict(name=pin["model"])])
                def evaluate(args,key,pin,contract,case,tokenizer):
                    with lock:active[0]+=1;maximum[0]=max(maximum[0],active[0]);calls.append(case["case_id"])
                    start=time.monotonic_ns();time.sleep(.005 if case["case_id"]=="long-01" else .02)
                    with lock:active[0]-=1
                    error="runtime_prompt_eval_count_mismatch" if fatal and case["case_id"]=="long-01" else None
                    return dict(case_id=case["case_id"],stage_ms={},generation_attempts=1,error=error,validation_error=None,
                                result=None if error else dict(summary="fixture",source_message_ids=["id"]),final_content="{}",generation_started_ns=start,
                                generation_finished_ns=time.monotonic_ns()),bool(error)
                with patch.object(client,"ROOT",root),patch.object(client,"assets",side_effect=assets),patch.object(client,"verify_show",side_effect=verify),patch.object(client,"call",side_effect=call),patch.object(client,"gpu",return_value=dict(fully_gpu=True,digest="pin")),patch.object(client,"evaluate",side_effect=evaluate):
                    result=profile.run_mode(client,args,pin,contract(),"concurrent2")
                self.assertEqual(len(set(calls)),len(calls));self.assertLessEqual(maximum[0],2)
                self.assertEqual(result["stages"]["tokenizer_engine_loads"],1)
                self.assertEqual(result["stages"]["tokenizer_json_loads"],1)
                self.assertEqual(result["stages"]["metadata_full_checks"],2)
                self.assertEqual(len(calls),2 if fatal else 30)
                self.assertEqual(result["http_overlap"],2)
                self.assertEqual(bool(result["error"]),fatal)
                self.assertTrue((root/"concurrent2/comparison.md").exists())
                if not fatal:self.assertEqual(result["successes"],30)


if __name__=="__main__":unittest.main()
