"""Separate CPU-only client: frozen Go contract, pinned per-model tokenizers, no weights."""
import argparse
import atexit
import datetime as dt
import hashlib
import html
import importlib.metadata
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "tools/alternative-models.json"
KNOWN = {"qwen3.5:27b-q4_K_M", "gpt-oss:20b", "gemma4:26b"}

def sha(data):
    return hashlib.sha256(data).hexdigest()

def bridge(binary, request):
    p = subprocess.run([str(binary)], input=json.dumps(request, ensure_ascii=False).encode(), stdout=subprocess.PIPE,
                       stderr=subprocess.PIPE, timeout=30, check=False)
    if p.returncode or len(p.stdout) > 8 * 1024 * 1024:
        raise ValueError("go_contract_failed")
    return json.loads(p.stdout)

def render(key, system, user, date):
    if key == "qwen-v2":
        return "<|im_start|>system\n"+system+"<|im_end|>\n<|im_start|>user\n"+user+"<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n"
    if key == "gpt-oss":
        return ("<|start|>system<|message|>You are ChatGPT, a large language model trained by OpenAI.\n"
                "Knowledge cutoff: 2024-06\nCurrent date: " + date +
                "\n\nReasoning: low\n\n# Valid channels: analysis, commentary, final. Channel must be included for every message."
                "<|end|><|start|>developer<|message|>\n\n# Instructions\n\n" + system +
                "<|end|><|start|>user<|message|>" + user + "<|end|><|start|>assistant")
    # 26B resolves legacy gemma4 -> gemma4-large (>=12B), including no-think block.
    return "<bos><|turn>system\n" + system + "<turn|>\n<|turn>user\n" + user + "<turn|>\n<|turn>model\n<|channel>thought\n<channel|>"

def call(base, path, payload=None, timeout=600):
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args):
            return None
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    request = urllib.request.Request(base + path, data=None if payload is None else json.dumps(payload, ensure_ascii=False).encode(),
                                     headers={"Content-Type": "application/json"})
    with opener.open(request, timeout=timeout) as response:
        raw = response.read(40 * 1024 * 1024 + 1)
    if len(raw) > 40 * 1024 * 1024:
        raise ValueError("response_too_large")
    result = json.loads(raw)
    if not isinstance(result, dict) or result.get("error"):
        raise ValueError("ollama_error")
    return result

def assets(key, manifest, prepare=False):
    directory = ROOT / "resources/tokenizer" / ("qwen35-27b" if key=="qwen-v2" else key)
    directory.mkdir(parents=True, exist_ok=True)
    for name, pin in manifest["sources"].items():
        path = directory / name
        if not path.exists() and prepare:
            with urllib.request.urlopen(pin["url"], timeout=120) as response:
                raw = response.read(pin["bytes"] + 1)
            if sha(raw) != pin["sha256"] or len(raw) != pin["bytes"]:
                raise ValueError("tokenizer_download_checksum_mismatch")
            path.write_bytes(raw)
        raw = path.read_bytes()
        if sha(raw) != pin["sha256"] or len(raw) != pin["bytes"]:
            raise ValueError("tokenizer_checksum_mismatch")
    from tokenizers import Tokenizer
    tokenizer = Tokenizer.from_file(str(directory / "tokenizer.json"))
    if key=="qwen-v2":tokenizer.normalizer=None
    # Retain Gemma's explicit space -> U+2581 mapping; GPT has no normalizer.
    # No Unicode normalization is introduced. Generation text is never changed.
    tokenizer.no_padding()
    tokenizer.no_truncation()
    return tokenizer

def verify_show(base, manifest):
    if call(base, "/api/version")["version"] != manifest["ollama_version"]:
        raise ValueError("ollama_version_mismatch")
    matches = [m for m in call(base, "/api/tags")["models"] if m.get("name") == manifest["model"]]
    if len(matches) != 1 or matches[0]["digest"] != manifest["digest"]:
        raise ValueError("model_digest_mismatch")
    show = call(base, "/api/show", {"model": manifest["model"], "verbose": True})
    if sha(show["template"].encode()) != manifest["template_sha256"]:
        raise ValueError("model_template_changed")
    metadata = {k: v for k, v in show["model_info"].items() if k.startswith("tokenizer.")}
    if sha(json.dumps(metadata, sort_keys=True, ensure_ascii=True, separators=(",", ":")).encode()) != manifest["tokenizer_metadata_sha256"]:
        raise ValueError("runtime_tokenizer_metadata_mismatch")
    key = {"gpt-oss:20b":"gpt-oss", "gemma4:26b":"gemma4", "qwen3.5:27b-q4_K_M":"qwen35-27b"}[manifest["model"]]
    tokenizer = json.loads((ROOT/"resources/tokenizer"/key/"tokenizer.json").read_bytes())
    vocab = metadata["tokenizer.ggml.tokens"]
    if (not all(vocab[i] == text for text,i in tokenizer["model"]["vocab"].items()) or
        metadata["tokenizer.ggml.merges"] != [" ".join(v) if isinstance(v,list) else v for v in tokenizer["model"]["merges"]] or
        not all(vocab[t["id"]] == t["content"] for t in tokenizer["added_tokens"])):
        raise ValueError("runtime_tokenizer_metadata_mismatch")
    return show.get("parameters")

def gpu(base, model, directory, key):
    cli = shutil.which("ollama")
    cli_status = "unavailable; /api/ps used (same source as ollama ps)"
    if cli:
        env = dict(os.environ, OLLAMA_HOST=base)
        p = subprocess.run([cli, "ps"], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
        (directory / (key + "-ollama-ps.txt")).write_bytes(p.stdout)
        if p.returncode:
            raise ValueError("ollama_ps_failed")
        cli_status = "ollama ps executed"
    models = call(base, "/api/ps").get("models", [])
    matches = [m for m in models if m.get("name") == model]
    if len(matches) != 1:
        raise ValueError("model_not_resident")
    m = matches[0]
    result = {k: m.get(k) for k in ("name", "digest", "size", "size_vram", "context_length")}
    result["cli_status"] = cli_status
    result["fully_gpu"] = isinstance(m.get("size"), int) and m["size"] > 0 and m.get("size_vram") == m["size"]
    return result

def unload(base, model):
    # Empty chat + keep_alive=0 is unload, not a generation. Only named diagnostics.
    if model not in KNOWN:
        raise ValueError("refuse_to_unload_unknown_model")
    call(base, "/api/chat", {"model": model, "messages": [], "keep_alive": 0, "stream": False})

def isolate(base):
    models = call(base, "/api/ps").get("models", [])
    if any(model["name"] not in KNOWN for model in models):
        raise ValueError("refuse_to_unload_unknown_model")
    for model in models:
        unload(base, model["name"])
    deadline = time.monotonic()+30
    while call(base, "/api/ps").get("models"):
        if time.monotonic() > deadline:
            raise ValueError("gpu_not_exclusive")
        time.sleep(.2)

def evaluate(args, key, manifest, contract, case, tokenizer):
    start = time.monotonic()
    reserve = manifest["reserve"]
    options = dict(temperature=.7, top_p=.8, top_k=20, min_p=0, presence_penalty=1.5, repeat_penalty=1,
                   num_ctx=8192, num_predict=reserve)
    payload = dict(model=manifest["model"], think=manifest["think"], stream=False, keep_alive="30m", truncate=False,
                   shift=False, options=options, format=contract["schema"], messages=[
                       {"role": "system", "content": contract["prompt"]}, {"role": "user", "content": case["user"]}])
    row = {"case_id": case["case_id"], "model": manifest["model"], "model_digest": manifest["digest"],
           "prompt_version": contract["prompt_version"], "schema_version": contract["schema_version"],
           "prompt_sha256": contract["prompt_sha256"], "schema_sha256": contract["schema_sha256"],
           "input_file_sha256": case["input_file_sha256"], "user_sha256": case["user_sha256"],
           "parameters": {k: v for k, v in payload.items() if k not in ("messages", "format")},
           "http_timeout_s": 600, "request_sha256": sha(json.dumps(payload, sort_keys=True, ensure_ascii=False).encode()),
           "baseline_report_sha256": args.baseline_hash, "manifest_sha256": args.manifest_hash,
           "qwen_v2_reference": case.get("qwen_baseline"), "generation_attempts": 0, "result": None,
           "final_content": None, "validation_error": None, "error": None,
           "recorded_at": dt.datetime.now(dt.timezone.utc).isoformat(),
           "comparison_note": "Models have different wrappers/tokenizers/think. GPT reserve=4096 includes reasoning; Gemma/Qwen reserve=1000. This does not isolate model or think alone."}
    if getattr(args,"suite",None)=="gemma-qwen-rest":
        row["comparison_note"]="Gemma/Qwen: same frozen v2 user/schema, think=false, baseline sampling and reserve=1000; native wrappers/tokenizers differ."
    fatal = False
    generated = False
    try:
        t = time.monotonic()
        row["model_default_parameters"] = verify_show(args.base_url, manifest)
        debug = call(args.base_url, "/api/chat", dict(payload, _debug_render_only=True))
        rendered = debug.get("_debug_info", {}).get("rendered_template")
        if not isinstance(rendered, str):
            raise ValueError("render_only_missing_rendered_template")
        date = debug.get("created_at", "")[:10]
        if key == "gpt-oss":
            day = dt.date.fromisoformat(date)
            if abs((day - dt.datetime.now(dt.timezone.utc).date()).days) > 1:
                raise ValueError("runtime_date_mismatch")
        if rendered != render(key, contract["prompt"], case["user"], date):
            raise ValueError("runtime_render_mismatch")
        row["rendered_sha256"] = sha(rendered.encode())
        row["render_check_ms"] = (time.monotonic()-t)*1000
        t = time.monotonic()
        tokens = tokenizer.encode(rendered, add_special_tokens=False).ids
        row["tokenizer_ms"] = (time.monotonic()-t)*1000
        row["budget"] = dict(input_tokens=len(tokens), output_reserve=reserve, context_tokens=8192,
                             required_tokens=len(tokens)+reserve, method="pinned_model_tokenizers_actual_render")
        if len(tokens)+reserve > 8192:
            raise ValueError("input_budget_exceeded")
        row["gpu_before"] = gpu(args.base_url, manifest["model"], args.out_dir, key)
        if not row["gpu_before"]["fully_gpu"]:
            raise ValueError("model_not_fully_gpu")
        verify_show(args.base_url, manifest)
        generated = True
        row["generation_attempts"] = 1
        t = time.monotonic()
        response = call(args.base_url, "/api/chat", payload)
        row["http_ms"] = (time.monotonic()-t)*1000
        message = response.get("message", {})
        thinking = message.pop("thinking", "")
        row["thinking_present"] = bool(thinking)
        row["thinking_bytes"] = len(thinking.encode())
        del thinking
        content = message.get("content", "")
        row["final_content"] = content
        for name in ("done", "done_reason", "prompt_eval_count", "eval_count"):
            row[name] = response.get(name)
        for name in ("total", "load", "prompt_eval", "eval"):
            duration = response.get(name + "_duration")
            row[name+"_ms"] = None if duration is None else duration/1000000
        row["validation_error"] = bridge(args.bridge, {"Action": "validate", "Input": case["input"], "Content": content})["error"]
        try:
            row["result"] = json.loads(content)
        except ValueError:
            pass
        row["runtime_token_count_verified"] = response.get("prompt_eval_count") == len(tokens)
        if not row["runtime_token_count_verified"]:
            fatal = True
            raise ValueError("runtime_prompt_eval_count_mismatch")
        if response.get("done") is not True or response.get("done_reason") != "stop" or not content.strip():
            raise ValueError("incomplete_response")
        if row["validation_error"]:
            raise ValueError("invalid_summary")
        row["gpu_after"] = gpu(args.base_url, manifest["model"], args.out_dir, key)
        if not row["gpu_after"]["fully_gpu"]:
            fatal = True
            raise ValueError("model_not_fully_gpu_after_generation")
    except Exception as error:
        fatal = fatal or not generated
        # Fixed codes only: never persist an HTTP response body or reasoning in errors.
        row["error"] = str(error) if type(error) is ValueError and str(error) in {
            "render_only_missing_rendered_template", "runtime_render_mismatch", "runtime_date_mismatch",
            "input_budget_exceeded", "model_not_fully_gpu", "model_not_fully_gpu_after_generation",
            "runtime_prompt_eval_count_mismatch", "incomplete_response", "invalid_summary",
            "model_digest_mismatch", "model_template_changed", "runtime_tokenizer_metadata_mismatch",
            "ollama_version_mismatch", "model_not_resident", "ollama_error"} else "diagnostic_transport_or_contract_error"
        row["result"] = None
    row["processing_ms"] = (time.monotonic()-start)*1000
    return row, fatal

def rest_reports(directory, cases, orchestration_error=None):
    ids=[c["case_id"] for c in cases]
    allrows={}
    summary={"successes":{},"generations":{},"errors":[],"orchestration_error":orchestration_error}
    lines=["# Gemma / Qwen v2 — syn-07…syn-24", "", "Смысловые оценки не вычислялись. Не запущенные строки явно отмечены.", "", "| Кейс | Gemma | Qwen v2 |", "|---|---|---|"]
    for key in ("gemma4","qwen-v2"):
        path=directory/(key+".jsonl")
        rows=[json.loads(line) for line in path.read_text(encoding="utf8").splitlines()] if path.exists() else []
        allrows[key]={r["case_id"]:r for r in rows}
        summary["successes"][key]=sum(r.get("generation_attempts")==1 and not r.get("error") and not r.get("validation_error") for r in rows)
        summary["generations"][key]=sum(r.get("generation_attempts",0) for r in rows)
        for r in rows:
            if r.get("error") or r.get("validation_error"):
                summary["errors"].append({"model":key,"case_id":r["case_id"],"error":r.get("error"),"validation_error":r.get("validation_error")})
    for id in ids:
        cells=[]
        for key in ("gemma4","qwen-v2"):
            r=allrows[key].get(id)
            if r is None:text="НЕ ЗАПУЩЕНО"
            elif r.get("error") or r.get("validation_error"):text="ОШИБКА: "+str(r.get("error") or r.get("validation_error"))+"\n"+(r.get("final_content") or "")
            else:text=r["result"]["summary"]
            cells.append(html.escape(text).replace("|","\\|").replace("\n","<br>"))
        lines.append("| "+id+" | "+" | ".join(cells)+" |")
    (directory/"comparison.md").write_text("\n".join(lines)+"\n",encoding="utf8")
    (directory/"summary.json").write_text(json.dumps(summary,ensure_ascii=False,indent=2),encoding="utf8")
    return summary

def run_rest(args,manifests,tokenizers,contract,manifest_path):
    expected=[f"syn-{i:02d}" for i in range(7,25)]
    if [c["case_id"] for c in contract["cases"]]!=expected or list(manifests)!=["gemma4","qwen-v2"]:
        raise ValueError("rest_selection_mismatch")
    plan=json.loads((args.out_dir/"plan.json").read_bytes())
    expected_pairs=[(m["model"],c["case_id"],c["user_sha256"],c["input_file_sha256"]) for m in manifests.values() for c in contract["cases"]]
    actual_pairs=[(r["model"],r["case_id"],r["user_sha256"],r.get("input_file_sha256")) for r in plan["cases"]]
    if actual_pairs!=expected_pairs or plan.get("manifest_sha256")!=args.manifest_hash or plan.get("prompt_sha256")!=contract["prompt_sha256"] or plan.get("schema_sha256")!=contract["schema_sha256"] or not all(r.get("budget_ok") for r in plan["cases"]):
        raise ValueError("successful_matching_36_case_plan_required")
    lock=args.out_dir/".generation.lock"
    fd=os.open(lock,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600);os.close(fd)
    failed=False;orchestration_error=None
    try:
        if any((args.out_dir/(key+".jsonl")).exists() for key in manifests):
            raise ValueError("outputs_exist_no_repeat")
        (args.out_dir/"manifest.json").write_bytes(manifest_path.read_bytes())
        for key in manifests:
            with (args.out_dir/(key+".jsonl")).open("x",encoding="utf8"):pass
        for key,m in manifests.items():
            isolate(args.base_url)
            try:
                for case in contract["cases"]:
                    row,fatal=evaluate(args,key,m,contract,case,tokenizers[key])
                    with (args.out_dir/(key+".jsonl")).open("a",encoding="utf8") as file:
                        file.write(json.dumps(row,ensure_ascii=False)+"\n");file.flush();os.fsync(file.fileno())
                    rest_reports(args.out_dir,contract["cases"])
                    print(key,case["case_id"],"ERROR "+row["error"] if row["error"] else "OK",flush=True)
                    failed=failed or bool(row["error"])
                    if fatal:return 1
            finally:unload(args.base_url,m["model"])
        return int(failed)
    except BaseException as error:
        orchestration_error=type(error).__name__
        raise
    finally:
        rest_reports(args.out_dir,contract["cases"],orchestration_error)
        lock.unlink(missing_ok=True)

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--bridge", type=Path, required=True)
    p.add_argument("--input-dir", required=True)
    p.add_argument("--baseline", type=Path)
    p.add_argument("--suite", choices=["first-six-alternatives","gemma-qwen-rest"], default="first-six-alternatives")
    p.add_argument("--out-dir", type=Path, required=True)
    p.add_argument("--base-url", default="http://127.0.0.1:11435", choices=["http://127.0.0.1:11435"])
    p.add_argument("--stage", choices=["probes", "gemma-probe", "remaining"], default="probes")
    p.add_argument("--prepare", action="store_true", help="explicit pinned tokenizer downloads, no Ollama")
    p.add_argument("--run", action="store_true")
    args = p.parse_args()
    if args.prepare and args.run:
        raise ValueError("prepare_and_run_are_separate")
    if sys.prefix == sys.base_prefix or importlib.metadata.version("tokenizers") != "0.22.1":
        raise ValueError("dedicated_tokenizers_0_22_1_venv_required")
    rest=args.suite=="gemma-qwen-rest"
    if rest and (args.baseline or args.stage!="probes"):raise ValueError("rest_suite_has_its_own_18_case_selection")
    if not rest and args.baseline is None:raise ValueError("qwen_baseline_required")
    manifest_path=ROOT/"tools/gemma-qwen-models.json" if rest else MANIFEST
    args.baseline_hash = None if rest else sha(args.baseline.read_bytes())
    args.manifest_hash = sha(manifest_path.read_bytes())
    contract = bridge(args.bridge.resolve(), {"Action": "prepare-rest" if rest else "prepare", "InputDir": str(Path(args.input_dir).resolve()), "Baseline": "" if rest else str(args.baseline.resolve())})
    args.bridge = args.bridge.resolve()
    args.out_dir.mkdir(parents=True, exist_ok=True)
    manifests = json.loads(manifest_path.read_bytes())
    tokenizers = {key: assets(key, m, args.prepare) for key, m in manifests.items()}
    if args.prepare:
        print("Pinned tokenizer assets verified; no generation.")
        return 0
    if not args.run:
        rows = []
        for key, m in manifests.items():
            if args.stage == "gemma-probe" and key != "gemma4":
                continue
            for case in contract["cases"]:
                if rest or (args.stage != "remaining") == (case["case_id"] == "syn-02"):
                    rendered=render(key,contract["prompt"],case["user"],dt.datetime.now(dt.timezone.utc).date().isoformat())
                    count=len(tokenizers[key].encode(rendered,add_special_tokens=False).ids)
                    rows.append(dict(model=m["model"],case_id=case["case_id"],think=m["think"],num_ctx=8192,
                                     output_reserve=m["reserve"],prompt_sha256=contract["prompt_sha256"],
                                     user_sha256=case["user_sha256"],input_file_sha256=case["input_file_sha256"],generation_attempts=0,
                                     input_tokens=count,required_tokens=count+m["reserve"],budget_ok=count+m["reserve"]<=8192,
                                     budget_status="candidate render; actual runtime render checked before generation"))
        plan={"stage":"plan","network":False,"cases":rows,"manifest_sha256":args.manifest_hash,"prompt_sha256":contract["prompt_sha256"],"schema_sha256":contract["schema_sha256"]}
        if rest:(args.out_dir/"plan.json").write_text(json.dumps(plan,ensure_ascii=False,indent=2),encoding="utf8")
        print(json.dumps(plan,ensure_ascii=False))
        return int(any(not r["budget_ok"] for r in rows))
    if rest:
        return run_rest(args,manifests,tokenizers,contract,manifest_path)
    lock = args.out_dir / ".generation.lock"
    fd = os.open(lock, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    os.close(fd)
    atexit.register(lambda: lock.unlink(missing_ok=True))
    snapshots = args.out_dir / "manifests"
    snapshots.mkdir(exist_ok=True)
    snapshot = snapshots / (args.manifest_hash+".json")
    if snapshot.exists() and sha(snapshot.read_bytes()) != args.manifest_hash:
        raise ValueError("manifest_snapshot_changed")
    snapshot.write_bytes(MANIFEST.read_bytes())
    def same_manifest(row, key):
        old = snapshots/(row.get("manifest_sha256", "missing")+".json")
        return old.exists() and sha(old.read_bytes()) == row["manifest_sha256"] and json.loads(old.read_bytes())[key] == manifests[key]
    # Read-only gate: both probes must be successful before ANY remaining calls.
    if args.stage == "remaining":
        for key, m in manifests.items():
            rows = [json.loads(line) for line in (args.out_dir/(key+".jsonl")).read_text(encoding="utf8").splitlines()]
            generations = [r for r in rows if r.get("generation_attempts") == 1]
            if len(generations)!=1:
                raise ValueError("both_successful_gpu_probes_required")
            row = generations[0]
            case2 = next(c for c in contract["cases"] if c["case_id"] == "syn-02")
            if row.get("case_id")!="syn-02" or row.get("model")!=m["model"] or row.get("model_digest")!=m["digest"] or row.get("error") or row.get("validation_error") or row.get("done") is not True or row.get("done_reason")!="stop" or row.get("prompt_sha256")!=contract["prompt_sha256"] or row.get("schema_sha256")!=contract["schema_sha256"] or row.get("user_sha256")!=case2["user_sha256"] or not row.get("runtime_token_count_verified") or not row.get("gpu_after",{}).get("fully_gpu") or row.get("baseline_report_sha256")!=args.baseline_hash or not same_manifest(row,key):
                raise ValueError("both_successful_gpu_probes_required")
            if bridge(args.bridge,{"Action":"validate","Input":case2["input"],"Content":row.get("final_content","")})["error"]:
                raise ValueError("invalid_probe_result")
    elif args.stage == "gemma-probe":
        # Explicit resume after a zero-generation renderer refusal only; never repeat GPT or Gemma generation.
        old_rows = [json.loads(line) for line in (args.out_dir/"gemma4.jsonl").read_text(encoding="utf8").splitlines()]
        if not old_rows or any(r.get("generation_attempts") != 0 for r in old_rows) or old_rows[-1].get("error") != "runtime_render_mismatch":
            raise ValueError("gemma_probe_already_generated_or_not_preflight_refusal")
    else:
        if any((args.out_dir/(key+".jsonl")).exists() for key in manifests):
            raise ValueError("probe_outputs_exist_no_repeat")
    failed = False
    for key, m in manifests.items():
        if args.stage == "gemma-probe" and key != "gemma4":
            continue
        isolate(args.base_url)
        try:
            for case in contract["cases"]:
                if (args.stage != "remaining") != (case["case_id"] == "syn-02"):
                    continue
                row, fatal = evaluate(args, key, m, contract, case, tokenizers[key])
                with (args.out_dir/(key+".jsonl")).open("a",encoding="utf8") as f:
                    f.write(json.dumps(row,ensure_ascii=False)+"\n");f.flush();os.fsync(f.fileno())
                print(key,case["case_id"],"ERROR "+row["error"] if row["error"] else "OK",flush=True)
                failed = failed or bool(row["error"])
                if fatal:
                    if args.stage == "remaining":
                        return 1
                    break
        finally:
            unload(args.base_url, m["model"])
    return int(failed)

if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as e:
        print("Diagnostic setup failed ("+type(e).__name__+"). No automatic repeat.",file=sys.stderr)
        sys.exit(1)
