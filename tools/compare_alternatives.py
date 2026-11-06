"""Separate CPU-only client: frozen Go contract, pinned per-model tokenizers, no weights."""
import argparse
import atexit
import contextlib
import datetime as dt
import hashlib
import html
import importlib.metadata
import json
import math
import os
from pathlib import Path
import shutil
import statistics
import subprocess
import sys
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "tools/alternative-models.json"
KNOWN = {"qwen3.5:27b-q4_K_M", "gpt-oss:20b", "gemma4:26b"}

def sha(data):
    return hashlib.sha256(data).hexdigest()

@contextlib.contextmanager
def measured(metrics, key):
    start = time.monotonic()
    try:
        yield
    finally:
        metrics[key] = metrics.get(key, 0)+(time.monotonic()-start)*1000

def bridge(binary, request, telemetry=None):
    metrics = telemetry if telemetry is not None else {}
    raw = json.dumps(request, ensure_ascii=False).encode()
    with measured(metrics, "helper_spawn_ms"):
        process = subprocess.Popen([str(binary)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    metrics["helper_spawns"] = metrics.get("helper_spawns", 0)+1
    try:
        with measured(metrics, "helper_work_wait_ms"):
            stdout, stderr = process.communicate(raw, timeout=30)
    except BaseException:
        process.kill(); process.communicate()
        raise
    if process.returncode or len(stdout) > 8 * 1024 * 1024:
        raise ValueError("go_contract_failed")
    result = json.loads(stdout)
    if "validation_ms" in result:
        metrics["go_validation_ms"] = result["validation_ms"]
    return result

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

def assets(key, manifest, prepare=False, telemetry=None):
    metrics = telemetry if telemetry is not None else {}
    start = time.monotonic()
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
    metrics["resource_checksum_ms"] = (time.monotonic()-start)*1000
    with measured(metrics, "tokenizers_import_ms"):
        from tokenizers import Tokenizer
    with measured(metrics, "tokenizer_engine_load_ms"):
        tokenizer = Tokenizer.from_file(str(directory / "tokenizer.json"))
    metrics["tokenizer_engine_loads"] = metrics.get("tokenizer_engine_loads", 0)+1
    if key=="qwen-v2":tokenizer.normalizer=None
    # Retain Gemma's explicit space -> U+2581 mapping; GPT has no normalizer.
    # No Unicode normalization is introduced. Generation text is never changed.
    tokenizer.no_padding()
    tokenizer.no_truncation()
    return tokenizer

def verify_show(base, manifest, telemetry=None, local_tokenizer=None):
    metrics = telemetry if telemetry is not None else {}
    start = time.monotonic()
    if call(base, "/api/version")["version"] != manifest["ollama_version"]:
        raise ValueError("ollama_version_mismatch")
    matches = [m for m in call(base, "/api/tags")["models"] if m.get("name") == manifest["model"]]
    if len(matches) != 1 or matches[0]["digest"] != manifest["digest"]:
        raise ValueError("model_digest_mismatch")
    metrics["model_identity_ms"] = metrics.get("model_identity_ms",0)+(time.monotonic()-start)*1000
    with measured(metrics, "metadata_http_ms"):
        show = call(base, "/api/show", {"model": manifest["model"], "verbose": True})
    metrics["metadata_full_checks"] = metrics.get("metadata_full_checks",0)+1
    compare_start = time.monotonic()
    if sha(show["template"].encode()) != manifest["template_sha256"]:
        raise ValueError("model_template_changed")
    metadata = {k: v for k, v in show["model_info"].items() if k.startswith("tokenizer.")}
    if sha(json.dumps(metadata, sort_keys=True, ensure_ascii=True, separators=(",", ":")).encode()) != manifest["tokenizer_metadata_sha256"]:
        raise ValueError("runtime_tokenizer_metadata_mismatch")
    key = {"gpt-oss:20b":"gpt-oss", "gemma4:26b":"gemma4", "qwen3.5:27b-q4_K_M":"qwen35-27b"}[manifest["model"]]
    metrics["metadata_hash_ms"] = metrics.get("metadata_hash_ms",0)+(time.monotonic()-compare_start)*1000
    tokenizer = local_tokenizer
    if tokenizer is None:
        with measured(metrics, "tokenizer_json_load_ms"):
            tokenizer = json.loads((ROOT/"resources/tokenizer"/key/"tokenizer.json").read_bytes())
        metrics["tokenizer_json_loads"] = metrics.get("tokenizer_json_loads",0)+1
    compare_start = time.monotonic()
    vocab = metadata["tokenizer.ggml.tokens"]
    if (not all(vocab[i] == text for text,i in tokenizer["model"]["vocab"].items()) or
        metadata["tokenizer.ggml.merges"] != [" ".join(v) if isinstance(v,list) else v for v in tokenizer["model"]["merges"]] or
        not all(vocab[t["id"]] == t["content"] for t in tokenizer["added_tokens"])):
        raise ValueError("runtime_tokenizer_metadata_mismatch")
    metrics["vocab_compare_ms"] = metrics.get("vocab_compare_ms",0)+(time.monotonic()-compare_start)*1000
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
    metrics = {}
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
           "ollama_version": manifest.get("ollama_version"),
           "stage_ms": metrics,
           "comparison_note": "Models have different wrappers/tokenizers/think. GPT reserve=4096 includes reasoning; Gemma/Qwen reserve=1000. This does not isolate model or think alone."}
    if getattr(args,"suite",None)=="gemma-qwen-rest":
        row["comparison_note"]="Gemma/Qwen: same frozen v2 user/schema, think=false, baseline sampling and reserve=1000; native wrappers/tokenizers differ."
    if getattr(args,"suite",None)=="gemma30":
        row["comparison_note"]="30 synthetic mail inputs, frozen v2/schema/baseline sampling; technical checks do not assess meaning."
    fatal = False
    generated = False
    try:
        if getattr(args, "suite", None) == "gemma30" and not getattr(args,"profile_optimized",False):
            with measured(metrics,"gpu_status_ms"):
                row["resident_before_case"] = call(args.base_url, "/api/ps")
        t = time.monotonic()
        verifier = getattr(args,"profile_verifier",None) or verify_show
        with measured(metrics,"metadata_verify_ms"):
            row["model_default_parameters"] = verifier(args.base_url, manifest, metrics)
        debug_start = time.monotonic()
        debug = call(args.base_url, "/api/chat", dict(payload, _debug_render_only=True))
        row["render_only_http_ms"] = (time.monotonic()-debug_start)*1000
        metrics["render_only_ms"] = row["render_only_http_ms"]
        # Render-only may load the runner. Preserve its timing separately from generation.
        # Missing server metrics remain null; never label wall time as pure load time.
        row["render_only_ollama_ms"] = {
            name: None if debug.get(name+"_duration") is None else debug[name+"_duration"]/1000000
            for name in ("total", "load", "prompt_eval", "eval")}
        row["render_only_done_reason"] = debug.get("done_reason")
        if getattr(args, "suite", None) == "gemma30" and not getattr(args,"profile_optimized",False):
            with measured(metrics,"gpu_status_ms"):
                row["resident_after_render_only"] = call(args.base_url, "/api/ps")
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
        metrics["input_encode_ms"] = row["tokenizer_ms"]
        row["budget"] = dict(input_tokens=len(tokens), output_reserve=reserve, context_tokens=8192,
                             required_tokens=len(tokens)+reserve, method="pinned_model_tokenizers_actual_render")
        if len(tokens)+reserve > 8192:
            raise ValueError("input_budget_exceeded")
        with measured(metrics,"gpu_status_ms"):
            row["gpu_before"] = gpu(args.base_url, manifest["model"], args.out_dir, key)
        if not row["gpu_before"]["fully_gpu"]:
            raise ValueError("model_not_fully_gpu")
        if getattr(args,"profile_strict",False) and row["gpu_before"].get("digest")!=manifest["digest"]:
            raise ValueError("model_digest_mismatch")
        with measured(metrics,"metadata_verify_ms"):
            verifier(args.base_url, manifest, metrics)
        generated = True
        row["generation_attempts"] = 1
        t = time.monotonic()
        row["generation_started_ns"] = time.monotonic_ns()
        response = call(args.base_url, "/api/chat", payload)
        row["generation_finished_ns"] = time.monotonic_ns()
        row["http_ms"] = (time.monotonic()-t)*1000
        metrics["generation_http_ms"] = row["http_ms"]
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
        with measured(metrics,"validation_stage_ms"):
            row["validation_error"] = bridge(args.bridge, {"Action": "validate", "Input": case["input"], "Content": content},metrics)["error"]
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
        with measured(metrics,"gpu_status_ms"):
            row["gpu_after"] = gpu(args.base_url, manifest["model"], args.out_dir, key)
        if not row["gpu_after"]["fully_gpu"]:
            fatal = True
            raise ValueError("model_not_fully_gpu_after_generation")
        if getattr(args,"profile_strict",False) and row["gpu_after"].get("digest")!=manifest["digest"]:
            fatal = True
            raise ValueError("model_digest_mismatch")
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

def metric_stats(values):
    values = sorted(v for v in values if isinstance(v, (int, float)))
    return {"count": len(values), "sum_ms": sum(values) if values else None,
            "median_ms": statistics.median(values) if values else None,
            "p95_ms": values[math.ceil(.95*len(values))-1] if values else None}

def gemma30_reports(directory, contract, state):
    path = directory/"gemma4.jsonl"
    rows = [json.loads(line) for line in path.read_text(encoding="utf8").splitlines()] if path.exists() else []
    by_id = {r["case_id"]: r for r in rows}
    succeeded = [r for r in rows if r.get("generation_attempts")==1 and not r.get("error") and not r.get("validation_error")]
    elapsed = state.get("batch_ms", 0)
    complete = state.get("finished", False) and len(rows)==30 and len(succeeded)==30 and not state.get("orchestration_error")
    ms = lambda value: "нет метрики" if value is None else f"{value:.3f}"
    cell = lambda text: html.escape(str(text)).replace("|", "\\|").replace("\n", "<br>")
    lines = ["# Gemma — исходные письма и пересказ", "", "Синтетические данные. Автоматические проверки подтверждают структуру, не смысловую точность. Разметка не читалась и модели не передавалась.", ""]
    for case in contract["cases"]:
        row = by_id.get(case["case_id"])
        letters = []
        for field, label in (("context_messages", "Предыстория"), ("messages", "Период")):
            for message in case["input"].get(field) or []:
                letters.append(f"**{label}; {message['id']}; {message['sent_at']}; {message['sender_role']}; {message['channel']}**<br>"+cell(message["text"]))
        if row is None:
            answer = "НЕ ЗАПУЩЕНО"
        elif row.get("error") or row.get("validation_error"):
            answer = cell("ОШИБКА: "+str(row.get("error") or row.get("validation_error"))+"\n"+(row.get("final_content") or ""))
        else:
            answer = cell(row["result"]["summary"])+"<br><br>Источники: "+cell(", ".join(row["result"]["source_message_ids"]))
        lines += ["## "+case["case_id"], "", "| Исходные письма | Ответ Gemma |", "|---|---|", "| "+"<br><br>".join(letters)+" | "+answer+" |", ""]
    with measured(state.setdefault("report_stage_ms",{}),"comparison_markdown_ms"):
        (directory/"comparison.md").write_text("\n".join(lines), encoding="utf8")
    stats = {"case_until_fsync": metric_stats([r["case_ms"] for r in state.get("timings", [])]),
             "generation_http": metric_stats([r.get("http_ms") for r in rows]),
             "ollama_total": metric_stats([r.get("total_ms") for r in rows]),
             "ollama_load": metric_stats([r.get("load_ms") for r in rows]),
             "ollama_prompt_eval": metric_stats([r.get("prompt_eval_ms") for r in rows]),
             "ollama_generation": metric_stats([r.get("eval_ms") for r in rows]),
             "render_http": metric_stats([r.get("render_only_http_ms") for r in rows]),
             "render_load": metric_stats([r.get("render_only_ollama_ms", {}).get("load") for r in rows])}
    success_rate = len(succeeded)*3600000/elapsed if elapsed else None
    state.update(successes=len(succeeded), generations=sum(r.get("generation_attempts", 0) for r in rows),
                 complete=complete, stats=stats, successful_summaries_per_hour=success_rate,
                 errors=[{"case_id":r["case_id"],"error":r.get("error"),"validation_error":r.get("validation_error")} for r in rows if r.get("error") or r.get("validation_error")])
    warm_rows = rows[1:] if state.get("first_case_initially_cold") else rows
    warm_windows = state.get("timings", [])[1:] if state.get("first_case_initially_cold") else state.get("timings", [])
    warm = complete and bool(warm_rows) and all(any(m.get("name")==state["model"] for m in r.get("resident_before_case", {}).get("models", [])) for r in warm_rows)
    warm_ms = warm_windows[-1]["saved_offset_ms"]-warm_windows[0]["started_offset_ms"] if warm and warm_windows else None
    state["warm_ms"] = warm_ms
    state["warm_cases"] = len(warm_rows) if warm else 0
    state["warm_successful_summaries_per_hour"] = len(warm_rows)*3600000/warm_ms if warm_ms else None
    (directory/"batch-state.json").write_text(json.dumps(state,ensure_ascii=False,indent=2),encoding="utf8")
    lines = ["# Gemma 4 26B — фактический последовательный benchmark", "",
             "**"+("Завершено 30/30" if complete else "ЧАСТИЧНЫЙ ПРОГОН — не время всех 30")+"**", "",
             f"Модель: `{state['model']}`; digest `{state['digest']}`; Ollama `{state['ollama_version']}`.",
             "Промпт `diagnostic-summary-v2`, схема `summary-v1`, think=false. Temperature=0.7, top_p=0.8, top_k=20, min_p=0, presence_penalty=1.5, repeat_penalty=1; context=8192, reserve/num_predict=1000, HTTP timeout=600s. По одному запросу, без повторов.", "",
             f"Начатых генераций: **{state['generations']}**, технически успешных: **{len(succeeded)}**, записей: {len(rows)}/30.",
             f"План без сети: **{ms(state.get('plan_ms'))} ms** (main → проверка входов/ресурсов/CPU-подсчёт; отдельно от реальной партии).",
             f"Полное время партии: **{elapsed/1000:.3f} s**; успешных обзоров в час: **{ms(success_rate)}**.",
             "Часы: time.monotonic(). От начала первого кейса до завершения flush+fsync последней JSONL-строки; включены проверки, загрузка, ожидание, tokenizers, HTTP/туннель, Go-валидация, запись, межкейсные обновления отчётов. Финальное построение Markdown вне этого интервала. Начальное /api/ps и план вне партии. Загрузка из времени не вычиталась.", "",
             "## Холодное начало и длительности", "",
             "Начальное состояние `/api/ps` сохранено в `batch-state.json`. Первая модель "+("отсутствовала в /api/ps — холодное начало." if state.get("first_case_initially_cold") else "была резидентна — холодная загрузка не измерена."),
             "Render-only может загрузить runner до генерации. Его HTTP-время и доступные server load metrics записаны отдельно; отсутствие load_duration не заменяется нулём и HTTP render-only не объявляется чистой загрузкой. `load_ms` ниже относится к генерационному ответу, это не автоматически вся холодная загрузка.", "",
             "| Метрика | Измерений | Сумма ms | Медиана ms | p95 ms |", "|---|---:|---:|---:|---:|"]
    for name, values in stats.items():
        lines.append(f"| {name} | {values['count']} | {ms(values['sum_ms'])} | {ms(values['median_ms'])} | {ms(values['p95_ms'])} |")
    if rows:
        first = rows[0]
        lines += ["", f"Первый кейс `{first['case_id']}`: render-only HTTP={ms(first.get('render_only_http_ms'))} ms; render-only load={ms(first.get('render_only_ollama_ms',{}).get('load'))} ms; generation HTTP={ms(first.get('http_ms'))} ms; generation load={ms(first.get('load_ms'))} ms; prompt_eval={ms(first.get('prompt_eval_ms'))} ms; generation={ms(first.get('eval_ms'))} ms."]
    if state.get("cold_load"):
        cold = state["cold_load"]
        lines += ["", f"Отдельная холодная загрузка внутри первого кейса: **{ms(cold['http_ms'])} ms HTTP**, server load_duration={ms(cold['ollama_ms']['load'])} ms, total_duration={ms(cold['ollama_ms']['total'])} ms; done_reason={cold['done_reason']}, eval_count={cold['eval_count']}. Это empty-chat load без генерации и без предварительного вычитания из партии. Если сервер не сообщает load_duration, HTTP отражает операцию загрузки целиком, включая туннель и ожидание, а не чистое время GPU.", "Документированный empty-chat load: [Ollama API](https://github.com/ollama/ollama/blob/main/docs/api.md#load-a-model-1). Загрузка выполняется только если начальный /api/ps не содержал Gemma; модель искусственно не выгружается."]
    measured_http = sum(r.get("http_ms",0)+r.get("render_only_http_ms",0) for r in rows)+state.get("cold_load",{}).get("http_ms",0)
    if rows and stats["generation_http"]["count"]==len(rows) and stats["render_http"]["count"]==len(rows):
        lines += ["", f"Остальное клиентское время партии: **{(elapsed-measured_http)/1000:.3f} s** (полная партия минус неперекрывающиеся HTTP генераций, render-only и отдельной загрузки). Это включает передачи/проверки метаданных, tokenizers, Go-валидацию и запись; серверные load/prompt_eval/eval уже входят в HTTP и повторно не прибавляются. Поэтому обзоры/час здесь — производительность всего диагностического клиента, а не максимальная скорость GPU или рабочего API."]
    lines += ["", "p95 — nearest rank: сортировка и элемент ceil(0.95 × N). Метрики Ollama — серверные наносекунды / 1e6; недоступные значения явно отсутствуют. Клиентский HTTP и серверные длительности перекрываются, их нельзя суммировать. GPU — reported size_vram==size через /api/ps, не независимый аудит размещения MoE-экспертов.", "", "## Прогноз одной RTX 4090", ""]
    if state.get("failed_before_first_result"):
        lines += ["Сбой до первой строки JSONL: показанное время измерено до ошибки, а не до несуществующего сохранённого ответа.", ""]
    if complete:
        lines += [f"Вся партия: {len(succeeded)} успешных / {elapsed/1000:.3f} s. "+(f"Тёплый участок: {len(warm_rows)} / {warm_ms/1000:.3f} s, {state['warm_successful_summaries_per_hour']:.3f} обзоров/час." if warm_ms else "Непрерывный тёплый участок не подтверждён."), "", "Тёплый участок определяется наличием Gemma в /api/ps перед кейсами; внутренние перезагрузки runner этим не исключены, их затраты из времени не вычитаются.", "", "| Клиентов | По всей партии, часы | По тёплому участку, часы |", "|---:|---:|---:|"]
        for n in (1000,3000,5000):
            lines.append(f"| {n} | {n*elapsed/len(succeeded)/3600000:.3f} | "+(f"{n*warm_ms/len(warm_rows)/3600000:.3f}" if warm_ms else "нет оценки")+" |")
        lines += ["", "Линейная оценка: N × (измеренное время / число успешных) / 3 600 000. Вариант всей партии сохраняет долю холодного старта 1/30; тёплый вариант исключает первый кейс только при подтверждённой резидентности остальных. Это ориентиры для такого же объёма и длины ответов, одной GPU, одного последовательного клиента и прежнего туннеля/проверок; не прогноз для произвольной реальной почты. Загрузка, другие пользователи GPU и распределение длин могут изменить время. Параллельное ускорение не экстраполируется."]
    else:
        lines += ["Прогноз для 1000/3000/5000 клиентов не рассчитывается: нет 30 успешных результатов."]
    lines += ["", "## По кейсам", "", "| Кейс | Ошибка | Client HTTP ms | Ollama ms | Load ms | Input eval ms | Generation ms | Input tokens | Output tokens |", "|---|---|---:|---:|---:|---:|---:|---:|---:|"]
    for row in rows:
        lines.append("| "+" | ".join([row["case_id"],cell(row.get("error") or row.get("validation_error") or "нет")]+[ms(row.get(k)) for k in ("http_ms","total_ms","load_ms","prompt_eval_ms","eval_ms")]+[str(row.get(k)) for k in ("prompt_eval_count","eval_count")])+" |")
    lines += ["", "Смысловое качество оценивается вручную по comparison.md. Технически успешный JSON не доказывает точность пересказа.", ""]
    if state.get("orchestration_error"): lines += ["Ошибка организации прогона: "+state["orchestration_error"]]
    with measured(state.setdefault("report_stage_ms",{}),"benchmark_markdown_ms"):
        (directory/"benchmark.md").write_text("\n".join(lines),encoding="utf8")

def run_gemma30(args, manifest, tokenizer, contract, manifest_path):
    expected = [f"long-{i:02d}" for i in range(1,31)]
    if [c["case_id"] for c in contract["cases"]] != expected:
        raise ValueError("gemma30_selection_mismatch")
    plan = json.loads((args.out_dir/"plan.json").read_bytes())
    pairs = [(r["case_id"],r["user_sha256"],r["input_file_sha256"]) for r in plan["cases"]]
    if pairs != [(c["case_id"],c["user_sha256"],c["input_file_sha256"]) for c in contract["cases"]] or plan.get("manifest_sha256")!=args.manifest_hash or plan.get("prompt_sha256")!=contract["prompt_sha256"] or plan.get("schema_sha256")!=contract["schema_sha256"] or not all(r.get("budget_ok") and r["model"]==manifest["model"] for r in plan["cases"]):
        raise ValueError("successful_matching_30_case_plan_required")
    lock = args.out_dir/".generation.lock"
    fd = os.open(lock,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600); os.close(fd)
    state = None
    batch_start = None
    try:
        path = args.out_dir/"gemma4.jsonl"
        if path.exists(): raise ValueError("outputs_exist_no_repeat")
        initial = call(args.base_url,"/api/ps")
        state = dict(model=manifest["model"],digest=manifest["digest"],ollama_version=manifest["ollama_version"],
                     prompt_sha256=contract["prompt_sha256"],schema_sha256=contract["schema_sha256"],
                     plan_ms=plan.get("plan_ms"),initial_ps=initial,
                     first_case_initially_cold=not any(m.get("name")==manifest["model"] for m in initial.get("models",[])),
                     timings=[],batch_ms=0,finished=False,orchestration_error=None)
        if any(m.get("name")!=manifest["model"] for m in initial.get("models",[])):
            raise ValueError("other_model_resident_no_service_changes")
        (args.out_dir/"manifest.json").write_bytes(manifest_path.read_bytes())
        with path.open("x",encoding="utf8"): pass
        gemma30_reports(args.out_dir,contract,state)
        batch_start = time.monotonic()
        for index, case in enumerate(contract["cases"],1):
            started = time.monotonic()
            if index==1:
                batch_start=started
            if index==1 and state["first_case_initially_cold"]:
                verify_show(args.base_url, manifest)
                load_start=time.monotonic()
                loaded=call(args.base_url,"/api/chat",dict(model=manifest["model"],messages=[],stream=False,think=False,keep_alive="30m",
                            options=dict(temperature=.7,top_p=.8,top_k=20,min_p=0,presence_penalty=1.5,repeat_penalty=1,num_ctx=8192,num_predict=1000)))
                state["cold_load"]=dict(http_ms=(time.monotonic()-load_start)*1000,done_reason=loaded.get("done_reason"),eval_count=loaded.get("eval_count"),
                                        ollama_ms={name:None if loaded.get(name+"_duration") is None else loaded[name+"_duration"]/1000000 for name in ("total","load","prompt_eval","eval")})
                if loaded.get("done") is not True or loaded.get("done_reason")!="load" or loaded.get("eval_count",0)!=0 or loaded.get("message",{}).get("content") or loaded.get("message",{}).get("thinking"):
                    raise ValueError("empty_chat_load_not_confirmed_no_generation_retry")
            row, fatal = evaluate(args,"gemma4",manifest,contract,case,tokenizer)
            row["case_order"] = index
            row["started_batch_offset_ms"] = (started-batch_start)*1000
            with path.open("a",encoding="utf8") as file:
                file.write(json.dumps(row,ensure_ascii=False)+"\n"); file.flush(); os.fsync(file.fileno())
            saved = time.monotonic()
            state["batch_ms"] = (saved-batch_start)*1000
            state["timings"].append(dict(case_id=case["case_id"],started_offset_ms=(started-batch_start)*1000,
                                        saved_offset_ms=state["batch_ms"],case_ms=(saved-started)*1000))
            state["finished"] = index==30
            gemma30_reports(args.out_dir,contract,state)
            print("gemma4",case["case_id"],"ERROR "+row["error"] if row["error"] else "OK",flush=True)
            if fatal: return 1
        return int(bool(state.get("errors")))
    except BaseException as error:
        if state is not None:
            state["orchestration_error"] = str(error) if type(error) is ValueError else type(error).__name__
            if batch_start is not None and not state["timings"]:
                state["batch_ms"] = (time.monotonic()-batch_start)*1000
                state["failed_before_first_result"] = True
        raise
    finally:
        if state is not None: gemma30_reports(args.out_dir,contract,state)
        lock.unlink(missing_ok=True)

def main():
    plan_started = time.monotonic()
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--bridge", type=Path, required=True)
    p.add_argument("--input-dir", required=True)
    p.add_argument("--baseline", type=Path)
    p.add_argument("--suite", choices=["first-six-alternatives","gemma-qwen-rest","gemma30"], default="first-six-alternatives")
    p.add_argument("--out-dir", type=Path, required=True)
    p.add_argument("--base-url", default="http://127.0.0.1:11435", choices=["http://127.0.0.1:11435"])
    p.add_argument("--stage", choices=["probes", "gemma-probe", "remaining"], default="probes")
    p.add_argument("--prepare", action="store_true", help="explicit pinned tokenizer downloads, no Ollama")
    p.add_argument("--run", action="store_true")
    p.add_argument("--profile-gemma30",action="store_true",help="warm current/optimized comparison; concurrent2 only below 1500/hour")
    args = p.parse_args()
    if args.prepare and args.run:
        raise ValueError("prepare_and_run_are_separate")
    if sys.prefix == sys.base_prefix or importlib.metadata.version("tokenizers") != "0.22.1":
        raise ValueError("dedicated_tokenizers_0_22_1_venv_required")
    rest=args.suite=="gemma-qwen-rest"
    gemma30=args.suite=="gemma30"
    if args.profile_gemma30 and not gemma30:raise ValueError("profiling_requires_gemma30")
    fixed=rest or gemma30
    if fixed and (args.baseline or args.stage!="probes"):raise ValueError("fixed_suite_has_its_own_case_selection")
    if not fixed and args.baseline is None:raise ValueError("qwen_baseline_required")
    manifest_path=ROOT/"tools/gemma-qwen-models.json" if fixed else MANIFEST
    args.baseline_hash = None if fixed else sha(args.baseline.read_bytes())
    args.manifest_hash = sha(manifest_path.read_bytes())
    contract_request={"Action": "prepare-gemma30" if gemma30 else "prepare-rest" if rest else "prepare", "InputDir": str(Path(args.input_dir).resolve()), "Baseline": "" if fixed else str(args.baseline.resolve())}
    args.contract_telemetry={}
    contract = bridge(args.bridge.resolve(),contract_request,args.contract_telemetry) if args.profile_gemma30 else bridge(args.bridge.resolve(),contract_request)
    args.bridge = args.bridge.resolve()
    args.out_dir.mkdir(parents=True, exist_ok=True)
    manifests = json.loads(manifest_path.read_bytes())
    if gemma30: manifests = {"gemma4": manifests["gemma4"]}
    if args.profile_gemma30 and args.run:
        from profile_gemma30 import run_comparison
        return run_comparison(sys.modules[__name__],args,manifests["gemma4"],contract,manifest_path)
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
                if fixed or (args.stage != "remaining") == (case["case_id"] == "syn-02"):
                    rendered=render(key,contract["prompt"],case["user"],dt.datetime.now(dt.timezone.utc).date().isoformat())
                    count=len(tokenizers[key].encode(rendered,add_special_tokens=False).ids)
                    rows.append(dict(model=m["model"],case_id=case["case_id"],think=m["think"],num_ctx=8192,
                                     output_reserve=m["reserve"],prompt_sha256=contract["prompt_sha256"],
                                     user_sha256=case["user_sha256"],input_file_sha256=case["input_file_sha256"],generation_attempts=0,
                                     input_tokens=count,required_tokens=count+m["reserve"],budget_ok=count+m["reserve"]<=8192,
                                     budget_status="candidate render; actual runtime render checked before generation"))
        plan={"stage":"plan","network":False,"cases":rows,"manifest_sha256":args.manifest_hash,"prompt_sha256":contract["prompt_sha256"],"schema_sha256":contract["schema_sha256"],"plan_ms":(time.monotonic()-plan_started)*1000}
        if args.profile_gemma30:
            plan["profiling"]={"current":30,"optimized":30,"concurrent2_conditional":30,"threshold_per_hour":1500}
        if fixed:(args.out_dir/"plan.json").write_text(json.dumps(plan,ensure_ascii=False,indent=2),encoding="utf8")
        print(json.dumps(plan,ensure_ascii=False))
        return int(any(not r["budget_ok"] for r in rows))
    if rest:
        return run_rest(args,manifests,tokenizers,contract,manifest_path)
    if gemma30:
        return run_gemma30(args,manifests["gemma4"],tokenizers["gemma4"],contract,manifest_path)
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
