import argparse
import contextlib
import datetime as dt
import importlib.util
import io
import json
import http.server
import threading
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
    def test_gemma30_plan_is_offline_and_only_gemma(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);(root/"tools").mkdir()
            (root/"tools/gemma-qwen-models.json").write_text(json.dumps({"gemma4":dict(model="gemma4:26b",think=False,reserve=1000),"qwen-v2":dict(model="excluded")}))
            contract=dict(prompt="system",prompt_sha256="p",schema_sha256="s",cases=[dict(case_id=f"long-{i:02d}",user="user",user_sha256=str(i),input_file_sha256=str(i)) for i in range(1,31)])
            args=["diag","--bridge",str(root/"bridge"),"--input-dir",str(root),"--suite","gemma30","--out-dir",str(root/"out")]
            with patch.object(m,"ROOT",root),patch.object(m,"bridge",return_value=contract) as bridge,patch.object(m,"assets",return_value=Tokens(100)) as assets,patch.object(m,"call",side_effect=AssertionError("network forbidden")),patch.object(m.sys,"prefix","test-venv"),patch.object(m.importlib.metadata,"version",return_value="0.22.1"),patch.object(sys,"argv",args),contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(m.main(),0)
                self.assertEqual(bridge.call_args.args[1]["Action"],"prepare-gemma30")
                self.assertEqual([c.args[0] for c in assets.call_args_list],["gemma4"])
            plan=json.loads((root/"out/plan.json").read_bytes())
            self.assertEqual(len(plan["cases"]),30)
            self.assertFalse(plan["network"])
            self.assertTrue(all(r["required_tokens"]==1100 and r["budget_ok"] and r["model"]=="gemma4:26b" for r in plan["cases"]))
            self.assertGreaterEqual(plan["plan_ms"],0)
        stats=m.metric_stats(list(range(1,31)))
        self.assertEqual(stats["median_ms"],15.5)
        self.assertEqual(stats["p95_ms"],29)

    def test_gemma30_batch_clock_reports_and_no_repeat(self):
        for scenario in ("ok", "invalid", "fatal"):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as directory:
                root=Path(directory)
                cases=[dict(case_id=f"long-{i:02d}",user="u",user_sha256=str(i),input_file_sha256=str(i),
                            input=dict(context_messages=[],messages=[dict(id="m",sent_at="date",sender_role="client",channel="email",text="ORIGINAL_LETTER")])) for i in range(1,31)]
                contract=dict(prompt="frozen",prompt_sha256="p",schema_sha256="s",cases=cases)
                manifest=dict(model="gemma4:26b",digest="d",ollama_version="0.40.15")
                args=argparse.Namespace(out_dir=root,manifest_hash="m",base_url="http://mock")
                plan=dict(plan_ms=123,manifest_sha256="m",prompt_sha256="p",schema_sha256="s",cases=[dict(model=manifest["model"],case_id=c["case_id"],user_sha256=c["user_sha256"],input_file_sha256=c["input_file_sha256"],budget_ok=True) for c in cases])
                (root/"plan.json").write_text(json.dumps(plan))
                pin=root/"source.json"; pin.write_text("{}")
                now=[0.0]; calls=[]
                def evaluate(args,key,pin,contract,case,tokenizer):
                    calls.append(case["case_id"])
                    now[0]+=3 if len(calls)==1 else 1
                    error="runtime_render_mismatch" if scenario=="fatal" else "invalid_summary" if scenario=="invalid" and len(calls)==1 else None
                    return dict(case_id=case["case_id"],generation_attempts=0 if scenario=="fatal" else 1,error=error,validation_error=error,
                                result=None if error else dict(summary="mock",source_message_ids=["m"]),
                                resident_before_case=dict(models=[] if len(calls)==1 else [dict(name=manifest["model"])]),final_content=None),scenario=="fatal"
                original=m.gemma30_reports
                def report(*args):
                    original(*args); now[0]+=.2
                def sync(*args): now[0]+=.01
                def cold_call(base,path,body=None):
                    if body is not None: now[0]+=7
                    return dict(done=True,done_reason="load",eval_count=0) if body is not None else dict(models=[])
                with patch.object(m,"call",side_effect=cold_call),patch.object(m,"verify_show"),patch.object(m,"evaluate",side_effect=evaluate),patch.object(m.time,"monotonic",side_effect=lambda:now[0]),patch.object(m.os,"fsync",side_effect=sync),patch.object(m,"gemma30_reports",side_effect=report):
                    self.assertEqual(m.run_gemma30(args,manifest,Tokens(100),contract,pin),0 if scenario=="ok" else 1)
                    saved=json.loads((root/"batch-state.json").read_bytes())
                    self.assertEqual(len(calls),1 if scenario=="fatal" else 30)
                    self.assertEqual(len(set(calls)),len(calls))
                    if scenario=="ok":
                        self.assertAlmostEqual(saved["batch_ms"],45100)
                        self.assertAlmostEqual(saved["warm_ms"],34890)
                        self.assertAlmostEqual(saved["cold_load"]["http_ms"],7000)
                        self.assertEqual(saved["successes"],30)
                    self.assertEqual(saved["complete"],scenario=="ok")
                    self.assertIn("ORIGINAL_LETTER",(root/"comparison.md").read_text(encoding="utf8"))
                    with self.assertRaisesRegex(ValueError,"outputs_exist_no_repeat"):
                        m.run_gemma30(args,manifest,Tokens(100),contract,pin)
                    self.assertEqual(len(calls),1 if scenario=="fatal" else 30)

    def test_gemma30_real_http_transport_and_load_metrics(self):
        requests=[]
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def reply(self,obj):
                raw=json.dumps(obj).encode();self.send_response(200);self.send_header("Content-Length",str(len(raw)));self.end_headers();self.wfile.write(raw)
            def do_GET(self):
                requests.append(self.path)
                self.reply(dict(models=[dict(name="gemma4:26b",size=100,size_vram=100)]))
            def do_POST(self):
                body=json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                requests.append(body)
                if body.get("_debug_render_only"):
                    self.reply(dict(_debug_info=dict(rendered_template=m.render("gemma4","system","user","")),load_duration=8000000000))
                else:
                    self.reply(dict(done=True,done_reason="stop",prompt_eval_count=100,eval_count=20,load_duration=3000000,prompt_eval_duration=4000000,eval_duration=5000000,total_duration=12000000,message=dict(content='{"summary":"mock","source_message_ids":["id"]}',thinking="PRIVATE_THINKING")))
        with tempfile.TemporaryDirectory() as directory, http.server.ThreadingHTTPServer(("127.0.0.1",0),Handler) as server:
            thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
            try:
                args=argparse.Namespace(base_url=f"http://127.0.0.1:{server.server_port}",suite="gemma30",bridge="unused",out_dir=Path(directory),baseline_hash=None,manifest_hash="m")
                contract=dict(prompt="system",prompt_version="v2",schema_version="summary-v1",prompt_sha256="s",schema_sha256="f",schema={})
                case=dict(case_id="long-01",user="user",input={},input_file_sha256="i",user_sha256="u")
                with patch.object(m,"verify_show",return_value="defaults"),patch.object(m,"bridge",return_value=dict(error=None)),patch.object(m.shutil,"which",return_value=None):
                    row,fatal=m.evaluate(args,"gemma4",dict(model="gemma4:26b",digest="pin",think=False,reserve=1000),contract,case,Tokens(100))
                self.assertIsNone(row["error"]);self.assertFalse(fatal)
                self.assertEqual(row["render_only_ollama_ms"]["load"],8000)
                self.assertEqual(row["load_ms"],3)
                self.assertIsNone(row["render_only_ollama_ms"]["eval"])
                self.assertNotIn("PRIVATE_THINKING",json.dumps(row))
                generations=[r for r in requests if isinstance(r,dict) and not r.get("_debug_render_only")]
                self.assertEqual(len(generations),1)
                self.assertFalse(generations[0]["think"])
            finally:server.shutdown();thread.join()

    def test_rest_suite_36_and_partial_failure(self):
        for scenario in ("ok","invalid","fatal"):
            with self.subTest(scenario=scenario),tempfile.TemporaryDirectory() as directory:
                root=Path(directory)
                cases=[dict(case_id=f"syn-{i:02d}",user="user"+str(i),user_sha256=str(i),input_file_sha256="file"+str(i),input={}) for i in range(7,25)]
                contract=dict(prompt="system",prompt_version="v2",schema={},schema_version="summary-v1",prompt_sha256="s",schema_sha256="f",cases=cases)
                args=argparse.Namespace(out_dir=root,manifest_hash="m",base_url="http://mock")
                manifests={"gemma4":dict(model="gemma4:26b"),"qwen-v2":dict(model="qwen3.5:27b-q4_K_M")}
                plan=dict(manifest_sha256="m",prompt_sha256="s",schema_sha256="f",cases=[dict(model=model["model"],case_id=c["case_id"],user_sha256=c["user_sha256"],input_file_sha256=c["input_file_sha256"],budget_ok=True) for model in manifests.values() for c in cases])
                (root/"plan.json").write_text(json.dumps(plan))
                manifest=root/"source.json";manifest.write_text("{}")
                calls=[]
                def evaluate(args,key,model,contract,case,tokenizer):
                    calls.append((key,case["case_id"]))
                    error="runtime_render_mismatch" if scenario=="fatal" else "invalid_summary" if scenario=="invalid" and len(calls)==1 else None
                    row=dict(case_id=case["case_id"],generation_attempts=0 if scenario=="fatal" else 1,error=error,validation_error=error,result=None if error else dict(summary="mock"),final_content=None)
                    return row,scenario=="fatal"
                with patch.object(m,"isolate"),patch.object(m,"unload"),patch.object(m,"evaluate",side_effect=evaluate):
                    self.assertEqual(m.run_rest(args,manifests,{k:Tokens(100) for k in manifests},contract,manifest),0 if scenario=="ok" else 1)
                    self.assertEqual(len(calls),1 if scenario=="fatal" else 36)
                    self.assertEqual(len(set(calls)),len(calls))
                    self.assertTrue(all(int(id[-2:])>=7 for _,id in calls))
                    self.assertTrue((root/"comparison.md").exists())
                    summary=json.loads((root/"summary.json").read_bytes())
                    self.assertEqual(summary["successes"]["qwen-v2"],0 if scenario=="fatal" else 18)
                    # Existing reports prohibit a second generation pass.
                    with self.assertRaisesRegex(ValueError,"outputs_exist_no_repeat"):m.run_rest(args,manifests,{k:Tokens(100) for k in manifests},contract,manifest)
                    self.assertEqual(len(calls),1 if scenario=="fatal" else 36)

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
        for key,think,reserve in [("gpt-oss","low",4096),("gemma4",False,1000),("qwen-v2",False,1000)]:
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
