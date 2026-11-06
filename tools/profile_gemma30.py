"""Profiling orchestration for the existing diagnostic client; no separate transport."""
import argparse
import concurrent.futures as futures
import json
import os
import time


def add(target, source):
    for key, value in source.items():
        if isinstance(value, (int, float)):
            target[key] = target.get(key, 0)+value


def warm(client, args, manifest, contract, tokenizer):
    start = time.monotonic()
    before = client.call(args.base_url, "/api/ps")
    if any(m["name"] != manifest["model"] for m in before.get("models", [])):
        raise ValueError("other_model_resident_no_service_changes")
    # No inference warmup and no unload. The normal schema/render checks load the same runner.
    payload = dict(model=manifest["model"],think=False,stream=False,keep_alive="30m",truncate=False,shift=False,
                   options=dict(temperature=.7,top_p=.8,top_k=20,min_p=0,presence_penalty=1.5,repeat_penalty=1,num_ctx=8192,num_predict=1000),
                   format=contract["schema"],messages=[dict(role="system",content=contract["prompt"]),dict(role="user",content=contract["cases"][0]["user"])],_debug_render_only=True)
    result = client.call(args.base_url,"/api/chat",payload)
    rendered = result.get("_debug_info",{}).get("rendered_template")
    if rendered != client.render("gemma4",contract["prompt"],contract["cases"][0]["user"],""):
        raise ValueError("runtime_render_mismatch")
    count = len(tokenizer.encode(rendered,add_special_tokens=False).ids)
    if count+1000>8192:raise ValueError("input_budget_exceeded")
    status = client.gpu(args.base_url,manifest["model"],args.out_dir,"gemma4")
    if not status["fully_gpu"] or status.get("digest")!=manifest["digest"]:
        raise ValueError("warm_gpu_or_digest_not_confirmed")
    return dict(warmup_ms=(time.monotonic()-start)*1000,initial_ps=before,gpu=status,
                note="resident GPU and exact render, no extra generation; inference/prefix-cache warmup not assumed")


def overlap(rows):
    events=[]
    for row in rows:
        if row.get("generation_started_ns") and row.get("generation_finished_ns"):
            events.extend([(row["generation_started_ns"],1),(row["generation_finished_ns"],-1)])
    current=maximum=0
    for _,delta in sorted(events):
        current+=delta;maximum=max(maximum,current)
    return maximum


def write_profile(client,directory,states,setup):
    lines=["# Gemma 30 — сравнение режимов на прогретой модели", "",
           "Неизменные gemma4:26b, diagnostic-summary-v2 / summary-v1, think=false, ctx8192/reserve1000; temperature=0.7, top_p=0.8, top_k=20, min_p=0, presence_penalty=1.5, repeat_penalty=1. Синтетические регрессии; структурная корректность не доказывает смысловую точность.",
           "Полное время тёплой партии включает подготовку токенизатора/checksum, проверки на границах партии, все кейсы, fsync JSONL, обновления отчётов и завершающий аудит. Прогрев GPU отдельно измерен и исключён. Cases s — от начала кейсов до fsync последнего JSONL. p95 — nearest rank; при двух запросах задержка кейса включает очередь клиента/сервера.",
           "Прогрев — только render-only, без дополнительных генераций. Без повторов, repair и изменения настроек сервера. /api/ps сообщает VRAM-размещение; независимый аудит MoE/nvidia-smi не выполнялся.", "",
           "| Режим | Успех | Полное s | Кейсы s | Обзоров/ч | p95 кейса ms | p95 HTTP ms | 3000 линейно, ч | 3000 одной партией, ч | HTTP одновременно |", "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|"]
    for state in states:
        complete=state["successes"]==30 and not state.get("error")
        rate=state["successes"]*3600000/state["full_ms"] if state["full_ms"] else 0
        forecast=f"{3000*state['full_ms']/30/3600000:.3f}" if complete else "partial"
        amortized=f"{(state['full_ms']-state['cases_ms']+3000*state['cases_ms']/30)/3600000:.3f}" if complete else "partial"
        p95case=state['case_stats'].get('p95_ms');p95http=state['http_stats'].get('p95_ms')
        p95case=f"{p95case:.3f}" if p95case is not None else "нет"
        p95http=f"{p95http:.3f}" if p95http is not None else "нет"
        lines.append(f"| {state['mode']} | {state['successes']}/30 | {state['full_ms']/1000:.3f} | {state['cases_ms']/1000:.3f} | {rate:.3f} | {p95case} | {p95http} | {forecast} | {amortized} | {state['http_overlap']} |")
    for state in states:
        if state["successes"]==30 and not state.get("error"):
            full_rate=30*3600000/state["full_ms"]
            cases_rate=30*3600000/state["cases_ms"]
            lines += ["", f"{state['mode']}: порог 1500 по полному времени "+("достигнут" if full_rate>=1500 else "не достигнут")+f"; {full_rate:.3f} обзоров/час. На одном интервале кейсов без фиксированной подготовки/финального аудита: {cases_rate:.3f} обзоров/час. Это отдельная метрика, не замена полного времени."]
    lines += ["", "## Суммы стадий, секунды", "", "| Стадия | "+" | ".join(s["mode"] for s in states)+" |", "|---|"+"---:|"*len(states)]
    labels=[("metadata_verify_ms","Проверка модели и метаданных"),
            ("tokenizer_engine_load_ms","Загрузка BPE engine"),("tokenizer_json_load_ms","Чтение/разбор tokenizer.json (вложено в metadata)"),
            ("render_only_ms","Render-only по входам"),("input_encode_ms","CPU-подсчёт токенов"),("gpu_status_ms","GPU/status проверки"),
            ("generation_http_ms","HTTP-генерация"),("validation_stage_ms","Валидация: helper + обмен + проверка"),
            ("helper_spawn_ms","Запуск helper (вложено)"),("helper_work_wait_ms","Ожидание работы/IPC helper (вложено)"),
            ("go_validation_ms","Структурная проверка внутри Go (вложено)"),
            ("jsonl_serialize_write_fsync_ms","JSONL: сериализация, запись, flush/fsync"),
            ("report_update_ms","Обновление Markdown + статистика/JSON состояния")]
    for key,label in labels:
        lines.append("| "+label+" | "+" | ".join(f"{s['stages'].get(key,0)/1000:.4f}" for s in states)+" |")
    lines += ["", "## Раздельные замеры и число загрузок", "",
              "Числовые стадии — сумма wall-time в ms, кроме полей-счётчиков. model_identity/metadata_http/hash/vocab/file JSON входят в metadata_verify_ms; helper_spawn/work_wait/go_validation вложены в validation_stage_ms. report_update_ms включает форматирование/запись Markdown и расчёт статистики/JSON состояния; report_*_markdown_ms измеряют непосредственно запись каждого Markdown-файла. Вложенные интервалы нельзя складывать повторно. При concurrent2 суммы перекрывающихся стадий могут превышать wall-time партии.",
              "Python-счётчик — прежний процесс клиента, Python-subprocess на каждый кейс отсутствует. tokenizer_engine_loads — Tokenizer.from_file, tokenizer_json_loads — отдельное чтение/разбор JSON словаря/merges. Go-helper валидации запускается на каждый кейс: отдельно Popen/CreateProcess и ожидание работы/IPC; go_validation_ms измерен внутри Go без старта/IPC.", ""]
    for state in states:
        lines += ["### "+state["mode"], "", f"Отдельный прогрев: {state['warmup']['warmup_ms']:.3f} ms. Проверки: полный аудит на границах + {state['verification_policy']}. Ошибки: {state.get('error') or state.get('case_errors') or 'нет'}. Токены input/output: {state.get('input_tokens')}/{state.get('output_tokens')}. p95 Ollama total: {state.get('ollama_stats',{}).get('p95_ms')} ms.", "", "| Стадия / счётчик | Значение |", "|---|---:|"]
        for key,value in sorted(state["stages"].items()):lines.append(f"| {key} | {value:.3f} |")
    lines += ["", "Current сохраняет обе полные проверки метаданных с разбором tokenizer.json на каждом кейсе. Optimized хранит разобранные метаданные и объект токенизатора в памяти, полностью проверяет ресурсы/метаданные на границах партии, проверяет digest резидентной модели и GPU до/после каждой генерации. Renderer, локальный бюджет, runtime prompt count, полнота ответа, JSON и источники проверяются на каждом входе. Убраны только избыточные снимки резидентности; необходимые GPU-проверки сохранены.",
              "Optimized обновляет Markdown в конце/при сбое; flush+fsync JSONL остаётся на каждый ответ. Частичный JSONL сохраняется. Ошибка завершающего аудита делает партию неуспешной. Fallback при несовместимости отсутствует.",
              "Прогноз 3000 «линейно»: 3000 × full_ms/30, с долей фиксированных затрат как в 30-кейсной партии. «Одной партией»: (full_ms − cases_ms) + 3000 × cases_ms/30, однократные проверки/подготовка амортизированы на все 3000. Оба прогноза линейны по наблюдаемым длинам, ответам, проверкам, туннелю и одной GPU; не SLA и не оценка произвольной реальной почты. Для двух запросов cases_ms/30 — интервал выпуска, а не индивидуальная задержка.",
              "Sampling неизменён, но число выходных токенов и prompt cache могут различаться. Порядок current → optimized → условный concurrent2 не рандомизирован. HTTP overlap подтверждает одновременные клиентские запросы, а не параллельные GPU slots. Ускорение не предполагается до измерения.", "",
              "Общая подготовка Go-контракта входов, вне партий: `"+json.dumps(setup,sort_keys=True)+"`.", ""]
    (directory/"profile.md").write_text("\n".join(lines),encoding="utf8")
    (directory/"profile.json").write_text(json.dumps(dict(setup=setup,runs=states),ensure_ascii=False,indent=2),encoding="utf8")


def run_mode(client,args,manifest,contract,mode):
    directory=args.out_dir/mode;directory.mkdir()
    local=argparse.Namespace(**vars(args));local.out_dir=directory
    local.profile_optimized=mode!="current";local.profile_strict=True
    workers=2 if mode in ("concurrent2","per_job_helper","persistent_helper") else 1
    cases=contract.get("jobs",contract["cases"])
    expected=len(cases)
    reporter=getattr(args,"profile_reporter",None) or client.gemma30_reports
    helper=None
    stages={};rows=[];windows=[];error=None;warmup={"warmup_ms":0}
    started=time.monotonic();cases_start=None;last_saved=None
    state=dict(model=manifest["model"],digest=manifest["digest"],ollama_version=manifest["ollama_version"],
               plan_ms=None,initial_ps={},first_case_initially_cold=False,timings=[],batch_ms=0,finished=False,orchestration_error=None)
    def reports():
        with client.measured(stages,"report_update_ms"):
            reporter(directory,contract,state)
    def persist(row,case_start):
        nonlocal last_saved
        row["profile_mode"]=mode
        with client.measured(stages,"jsonl_serialize_write_fsync_ms"):
            with (directory/"gemma4.jsonl").open("a",encoding="utf8") as output:
                output.write(json.dumps(row,ensure_ascii=False)+"\n");output.flush();os.fsync(output.fileno())
        last_saved=time.monotonic()
        windows.append(dict(case_id=row["case_id"],job_id=row.get("job_id"),started_offset_ms=(case_start-cases_start)*1000,
                            saved_offset_ms=(last_saved-cases_start)*1000,case_ms=(last_saved-case_start)*1000))
        rows.append(row);add(stages,row["stage_ms"])
        state.update(timings=windows,batch_ms=(last_saved-cases_start)*1000,finished=len(rows)==expected)
        if mode=="current":reports()
        if getattr(args,"repeat_helper300",False):
            if len(rows)%10==0 or row["error"]:
                reports()
                print(mode,f"{len(rows)}/{expected}","ERROR "+row["error"] if row["error"] else "OK",flush=True)
        else:print(mode,row["case_id"],"ERROR "+row["error"] if row["error"] else "OK",flush=True)
    def job(case,index):
        begin=time.monotonic()
        row,fatal=client.evaluate(local,"gemma4",manifest,contract,case,tokenizer)
        row["case_order"]=index
        return row,fatal,begin
    try:
        tokenizer=client.assets("gemma4",manifest,telemetry=stages)
        parsed=None
        if local.profile_optimized:
            with client.measured(stages,"tokenizer_json_load_ms"):
                parsed=json.loads((client.ROOT/"resources/tokenizer/gemma4/tokenizer.json").read_bytes())
            stages["tokenizer_json_loads"]=1
        with client.measured(stages,"metadata_verify_ms"):
            defaults=client.verify_show(args.base_url,manifest,stages,local_tokenizer=parsed)
        warmup=warm(client,local,manifest,contract,tokenizer)
        state["initial_ps"]=warmup["initial_ps"]
        def cached(base,pin,metrics):
            metrics["metadata_cached_hits"]=metrics.get("metadata_cached_hits",0)+1
            return defaults
        if local.profile_optimized:local.profile_verifier=cached
        if mode=="persistent_helper":
            if __package__:
                from .persistent_bridge import PersistentBridge
            else:
                from persistent_bridge import PersistentBridge
            helper=PersistentBridge([str(args.bridge),"--serve"],stages,args.comparison_id+":"+mode)
            local.profile_validator=lambda binary,request,metrics:helper.request(request,metrics)
        cases_start=time.monotonic()
        pending={};position=0;stop=False
        with futures.ThreadPoolExecutor(max_workers=workers) as pool:
            while pending or (position<len(cases) and not stop):
                while len(pending)<workers and position<len(cases) and not stop:
                    position+=1
                    pending[pool.submit(job,cases[position-1],position)]=position
                done,_=futures.wait(pending,return_when=futures.FIRST_COMPLETED)
                for future in sorted(done,key=lambda f:pending[f]):
                    pending.pop(future)
                    row,fatal,begin=future.result();persist(row,begin)
                    stop=stop or fatal
            if stop:error="compatibility_or_preflight_failure"
        # Static invariants are audited again before declaring the optimized run successful.
        with client.measured(stages,"metadata_verify_ms"):
            client.verify_show(args.base_url,manifest,stages,local_tokenizer=parsed)
        with client.measured(stages,"resource_checksum_end_ms"):
            for name,pin in manifest["sources"].items():
                raw=(client.ROOT/"resources/tokenizer/gemma4"/name).read_bytes()
                if len(raw)!=pin["bytes"] or client.sha(raw)!=pin["sha256"]:raise ValueError("tokenizer_checksum_mismatch")
    except Exception as failure:
        error=type(failure).__name__+":"+str(failure) if type(failure) is ValueError else type(failure).__name__
    finally:
        if helper is not None:
            try:
                with client.measured(stages,"helper_shutdown_ms"):helper.close()
            except Exception:error=error or "persistent_helper_shutdown_failed"
        state["orchestration_error"]=error
        reports();add(stages,{"report_"+k:v for k,v in state.get("report_stage_ms",{}).items()})
    ended=time.monotonic()
    full_ms=(ended-started)*1000-warmup["warmup_ms"]
    summary=dict(mode=mode,successes=sum(not r["error"] and not r["validation_error"] for r in rows),generations=sum(r["generation_attempts"] for r in rows),
                 full_ms=full_ms,cases_ms=(last_saved-cases_start)*1000 if last_saved else 0,
                 warmup=warmup,stages=stages,error=error,case_errors=[dict(case_id=r["case_id"],error=r["error"],validation_error=r["validation_error"]) for r in rows if r["error"] or r["validation_error"]],
                 case_stats=client.metric_stats([w["case_ms"] for w in windows]),http_stats=client.metric_stats([r.get("http_ms") for r in rows]),
                 ollama_stats=client.metric_stats([r.get("total_ms") for r in rows]),http_overlap=overlap(rows),
                 input_tokens=sum(r.get("prompt_eval_count") or 0 for r in rows),output_tokens=sum(r.get("eval_count") or 0 for r in rows),
                 verification_policy="two full checks per case" if mode=="current" else "per-input render/budget/resident digest/GPU/runtime count/JSON/sources")
    summary.update(expected_jobs=expected,fixed_prepare_ms=(cases_start-started)*1000-warmup["warmup_ms"] if cases_start else full_ms,
                   fixed_finalize_ms=(ended-last_saved)*1000 if last_saved else 0,
                   helper_pid=helper.pid if helper is not None else None,helper_exit_code=helper.process.returncode if helper is not None else None)
    (directory/"phase.json").write_text(json.dumps(summary,ensure_ascii=False,indent=2),encoding="utf8")
    return summary


def run_comparison(client,args,manifest,contract,manifest_path):
    plan=json.loads((args.out_dir/"plan.json").read_bytes())
    if len(contract["cases"])!=30 or [c["case_id"] for c in contract["cases"]]!=[f"long-{i:02d}" for i in range(1,31)]:raise ValueError("gemma30_selection_mismatch")
    if plan.get("profiling",{}).get("threshold_per_hour")!=1500 or plan["manifest_sha256"]!=args.manifest_hash or plan["prompt_sha256"]!=contract["prompt_sha256"] or plan["schema_sha256"]!=contract["schema_sha256"] or [(r["case_id"],r["user_sha256"],r["input_file_sha256"]) for r in plan["cases"]]!=[(c["case_id"],c["user_sha256"],c["input_file_sha256"]) for c in contract["cases"]] or not all(r["budget_ok"] for r in plan["cases"]):raise ValueError("matching_profile_plan_required")
    lock=args.out_dir/".generation.lock"
    fd=os.open(lock,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600);os.close(fd)
    states=[]
    try:
        if any((args.out_dir/name).exists() for name in ("current","optimized","concurrent2")):raise ValueError("profile_outputs_exist_no_repeat")
        (args.out_dir/"manifest.json").write_bytes(manifest_path.read_bytes())
        for mode in ("current","optimized","concurrent2"):
            if mode=="concurrent2" and states[-1]["successes"]==30 and not states[-1]["case_errors"] and 30*3600000/states[-1]["full_ms"]>=1500:break
            state=run_mode(client,args,manifest,contract,mode);states.append(state)
            write_profile(client,args.out_dir,states,args.contract_telemetry)
            if state["error"] or state["successes"]!=30:return 1
        return 0
    finally:
        write_profile(client,args.out_dir,states,args.contract_telemetry)
        lock.unlink(missing_ok=True)
