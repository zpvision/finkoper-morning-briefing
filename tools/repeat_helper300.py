"""300 jobs per mode over 30 repeated synthetic inputs, using the existing runner."""
import json
import html
import os
import platform
import uuid
if __package__:
    from . import profile_gemma30 as profile
else:
    import profile_gemma30 as profile


def jobs(contract,comparison_id,mode):
    result=[]
    for repeat in range(1,11):
        for offset,case in enumerate(contract["cases"],1):
            index=30*(repeat-1)+offset
            result.append(dict(case,job_id=f"{comparison_id}:{mode}:{index:06d}",job_index=index,repeat_index=repeat))
    return result


def rows_at(directory):
    path=directory/"gemma4.jsonl"
    return [json.loads(line) for line in path.read_text(encoding="utf8").splitlines()] if path.exists() else []


def progress(directory,contract,state):
    rows=rows_at(directory)
    errors=[dict(job_id=r["job_id"],case_id=r["case_id"],error=r["error"],validation_error=r["validation_error"]) for r in rows if r["error"] or r["validation_error"]]
    snapshot=dict(state,completed=len(rows),successes=len(rows)-len(errors),expected_jobs=300,
                  synthetic_repeat=True,unique_source_cases=30,repetitions=10,errors=errors)
    (directory/"progress.json").write_text(json.dumps(snapshot,ensure_ascii=False,indent=2),encoding="utf8")
    lines=["# Повтор 30 синтетических входов — 300 заданий", "", "Это десять повторов одних и тех же переписок, не 300 независимых клиентов. job_id не передаётся модели. Смысловая точность автоматически не оценивается.", "",
           f"Записано: {len(rows)}/300. Ошибок: {len(errors)}. Интервал заданий: {state.get('batch_ms',0)/1000:.3f} s.", "",
           "| job_id | Исходный кейс | Повтор | Ответ | Источники | Ошибка |", "|---|---|---:|---|---|---|"]
    def cell(value):return html.escape(str(value)).replace("|","\\|").replace("\n","<br>")
    for row in rows:
        result=row.get("result") or {}
        lines.append("| "+" | ".join(map(cell,[row["job_id"],row["case_id"],row["repeat_index"],result.get("summary") or row.get("final_content") or "",", ".join(result.get("source_message_ids",[])),row.get("error") or row.get("validation_error") or "нет"]))+" |")
    (directory/"results.md").write_text("\n".join(lines)+"\n",encoding="utf8")


def report(client,directory,states,comparison_id,setup):
    environment=setup.setdefault("client_environment",dict(os=platform.platform(),python=platform.python_version()))
    lines=["# Go-helper: повтор синтетических входов, два одновременных задания", "",
           "**Каждый режим: 300 заданий = десять повторов тех же 30 синтетических кейсов long-01…long-30. Это не 300 независимых переписок и не нагрузка реальных клиентов.**", "",
           "Модель gemma4:26b, закреплённые digest/Ollama/tokenizer/renderer; неизменные diagnostic-summary-v2 и summary-v1, think=false, ctx8192/reserve1000. Temperature=0.7, top_p=0.8, top_k=20, min_p=0, presence_penalty=1.5, repeat_penalty=1, stream=false, keep_alive=30m, timeout=600s. Рабочий API и глобальные настройки Ollama не менялись.",
           f"Клиент и Go-helper: {environment['os']}, Python {environment['python']}. Ollama/GPU — через действующий туннель http://127.0.0.1:11435 на арендованную RTX 4090. Время локального запуска Go-процесса не является временем CPU сервера.",
           "Сравнивается concurrent2 из f6e49b2 (Go-процесс на ответ) с тем же режимом и одним постоянным Go-helper. Оба сохраняют кэш статических ресурсов на партию. Все входы/ответы, runtime render, бюджет, runtime token count, GPU/digest, JSON и источники проверяются для каждого задания. Проверка модели/метаданных/checksum — на границах партии. Ответы и результаты валидации не переиспользуются.",
           "job_id уникальны во всём прогоне и остаются в диагностике/IPC, не меняют JSON модели. Каждый validation request имеет RequestID=job_id+:validate; ответ должен вернуть тот же ID и PID helper. У постоянного процесса есть отдельный ID readiness/ping. Без повторов, repair, fallback или автоматического перезапуска helper.", "",
           "| Режим | Успех | Полное s | Подготовка s | Задания s | Завершение s | Обзоров/ч | p95 задания s | p95 HTTP s | Ошибок |", "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|"]
    for state in states:
        errors=len(state["case_errors"])+(1 if state["error"] else 0)
        number=lambda value:"нет" if value is None else f"{value:.3f}"
        lines.append("| "+" | ".join([state["mode"],f"{state['successes']}/300",number(state["full_ms"]/1000),number(state["fixed_prepare_ms"]/1000),number(state["cases_ms"]/1000),number(state["fixed_finalize_ms"]/1000),number(state["successes"]*3600000/state["full_ms"]),number(state["case_stats"]["p95_ms"]/1000 if state["case_stats"]["p95_ms"] is not None else None),number(state["http_stats"]["p95_ms"]/1000 if state["http_stats"]["p95_ms"] is not None else None),str(errors)])+" |")
    lines += ["", "Полное тёплое время = фиксированная подготовка + интервал заданий до последнего fsync JSONL + финальные аудиты/закрытие helper/Markdown. GPU-прогрев (render-only, без дополнительной генерации) отдельно измерен и исключён. Подготовка включает BPE, JSON/checksum, первый полный аудит и запуск/handshake постоянного helper. Последний Go-процесс одноразового режима завершается внутри последнего задания. p95 — nearest rank до записи ответа, включая ожидание; не средний интервал выпуска. Общая подготовка Go-контракта вне двух партий показана ниже. Итоговые фазовые/сравнительные JSON-агрегаты пишутся после таймера. При ошибке результаты частичные.", ""]
    if len(states)==2 and all(s["successes"]==300 and not s["error"] for s in states):
        change=(1-states[1]["full_ms"]/states[0]["full_ms"])*100
        change_text=f"{abs(change):.2f}% "+("быстрее" if change>=0 else "медленнее")
        lines += [f"Наблюдаемое изменение полного времени: **{change_text}** у persistent_helper. Это один упорядоченный прогон, не статистически подтверждённое ускорение и не доказательство влияния только IPC.",
                  "Суммарное время стадий перекрывается при двух заданиях: устранённые секунды запуска helper нельзя напрямую вычесть из wall-time партии. Более ранняя готовность следующего запроса может увеличить ожидание HTTP/Ollama.", ""]
    for state in states:
        mode=state["mode"];rows=rows_at(directory/mode)
        distinct=len({r["user_sha256"] for r in rows})
        cache_rows=[r for r in rows if r.get("cache_signals")]
        pids={r.get("validation_helper_pid") for r in rows if r.get("validation_helper_pid") is not None}
        stages=state["stages"]
        lines += ["## "+mode, "", f"Прогрев отдельно: {state['warmup']['warmup_ms']/1000:.3f} s. Наблюдаемый HTTP overlap: {state['http_overlap']}. Токены input/output: {state['input_tokens']}/{state['output_tokens']}. Ошибка партии: {state['error'] or 'нет'}.",
                  f"Файлы: [JSONL]({mode}/gemma4.jsonl), [ответы по job_id]({mode}/results.md), [метрики]({mode}/phase.json).",
                  f"Запусков validation helper: {stages.get('helper_spawns',0)}; ответов helper с корреляцией ID: {len(rows)-sum(r.get('validation_request_id') is None for r in rows)}; наблюдаемых PID: {len(pids)}. PID постоянного helper: {state.get('helper_pid')}, exit={state.get('helper_exit_code')}. Один ping учитывается отдельно: {stages.get('helper_control_requests',0)}.",
                  f"Признаки локального кэша: engine loads={stages.get('tokenizer_engine_loads',0)}, tokenizer JSON parses={stages.get('tokenizer_json_loads',0)}, metadata cached hits={stages.get('metadata_cached_hits',0)}, полных metadata audits={stages.get('metadata_full_checks',0)}.",
                  f"Уникальных user JSON hashes: {distinct}; повторных запросов с уже встреченным входом: {len(rows)-distinct}. Записей с явными числовыми/boolean cache-полями сервера из сохраняемого набора: {len(cache_rows)}/{len(rows)}. "+("Поля сохранены как cache_signals в JSONL; интерпретация зависит от runtime." if cache_rows else "В сохранённых метриках нет прямого счётчика prefix/KV cache — попадания в серверный кэш не считаются подтверждёнными."), "",
                  "| Повтор | Успех | Медиана prompt_eval ms | p95 prompt_eval ms | Медиана HTTP ms | Output tokens |", "|---:|---:|---:|---:|---:|---:|"]
        for repetition in range(1,11):
            group=[r for r in rows if r["repeat_index"]==repetition]
            if not group:continue
            prompt=client.metric_stats([r.get("prompt_eval_ms") for r in group]);http=client.metric_stats([r.get("http_ms") for r in group])
            fmt=lambda v:"нет" if v is None else f"{v:.3f}"
            lines.append(f"| {repetition} | {sum(not r['error'] and not r['validation_error'] for r in group)}/{len(group)} | {fmt(prompt['median_ms'])} | {fmt(prompt['p95_ms'])} | {fmt(http['median_ms'])} | {sum(r.get('eval_count') or 0 for r in group)} |")
        lines += ["", "| Стадия / счётчик | Значение ms / число |", "|---|---:|"]
        for key,value in sorted(stages.items()):lines.append(f"| {key} | {value:.4f} |")
        lines += ["", "| Метрика Ollama / HTTP | Сумма s |", "|---|---:|"]
        for key in ("http_ms","total_ms","load_ms","prompt_eval_ms","eval_ms"):
            values=[r[key] for r in rows if isinstance(r.get(key),(int,float))]
            lines.append(f"| {key} | "+(f"{sum(values)/1000:.4f}" if values else "нет метрики")+" |")
        if state["case_errors"]:lines += ["", "Ошибки заданий: `"+json.dumps(state["case_errors"],ensure_ascii=False)+"`"]
    lines += ["", "## Ограничения интерпретации", "",
              "Одинаковые hashes доказывают повтор входов и возможность повторного использования prefix cache, но не попадание в KV cache. Изменения prompt_eval_duration при одинаковых входах — косвенный признак: на них также влияют прогрев, серверная очередь и другие условия. Явные cache-поля не предполагаются и не запрашиваются нестандартными опциями. Все генерации реальные; модельные результаты и валидация программой не кэшируются.",
              "Порядок режимов per_job_helper → persistent_helper, без сброса кэша и рандомизации. Sampling сохранён, но тексты/выходные токены могут отличаться. Повтор 30 входов может завысить оценку пропускной способности относительно новых разных клиентов. Это диагностический замер данного клиента/туннеля/одной GPU, не SLA и не независимая оценка качества. HTTP overlap=2 не доказывает два GPU slots. VRAM подтверждается только reported /api/ps.",
              "Вложенные стадии не суммируются дважды: helper spawn/work/Go validation входят в validation_stage_ms, metadata hash/parse/compare — в metadata_verify_ms. При двух заданиях интервалы перекрываются и сумма стадий может превышать wall-time партии.", "",
              f"comparison_id: `{comparison_id}`. Общая Go-подготовка входов: `"+json.dumps(setup,sort_keys=True)+"`.", ""]
    (directory/"benchmark.md").write_text("\n".join(lines),encoding="utf8")
    (directory/"benchmark.json").write_text(json.dumps(dict(comparison_id=comparison_id,synthetic_repeat=True,unique_source_cases=30,repetitions=10,setup=setup,runs=states),ensure_ascii=False,indent=2),encoding="utf8")


def run_comparison(client,args,manifest,contract,manifest_path):
    plan=json.loads((args.out_dir/"plan.json").read_bytes())
    if [c["case_id"] for c in contract["cases"]]!=[f"long-{i:02d}" for i in range(1,31)]:raise ValueError("gemma30_selection_mismatch")
    if plan.get("repeat_helper300")!=dict(per_job_helper=300,persistent_helper=300,concurrency=2,synthetic_repeat=True,repetitions=10,job_ids_outside_model_input=True) or plan["manifest_sha256"]!=args.manifest_hash or plan["prompt_sha256"]!=contract["prompt_sha256"] or plan["schema_sha256"]!=contract["schema_sha256"] or [(r["case_id"],r["user_sha256"],r["input_file_sha256"]) for r in plan["cases"]]!=[(c["case_id"],c["user_sha256"],c["input_file_sha256"]) for c in contract["cases"]] or not all(r["budget_ok"] for r in plan["cases"]):raise ValueError("matching_600_generation_plan_required")
    lock=args.out_dir/".generation.lock";fd=os.open(lock,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600);os.close(fd)
    args.comparison_id=uuid.uuid4().hex;args.profile_reporter=progress;states=[];claimed=False
    try:
        if any((args.out_dir/m).exists() for m in ("per_job_helper","persistent_helper")):raise ValueError("repeat_outputs_exist_no_retry")
        claimed=True
        (args.out_dir/"manifest.json").write_bytes(manifest_path.read_bytes())
        for mode in ("per_job_helper","persistent_helper"):
            expanded=dict(contract,jobs=jobs(contract,args.comparison_id,mode))
            state=profile.run_mode(client,args,manifest,expanded,mode);states.append(state)
            report(client,args.out_dir,states,args.comparison_id,args.contract_telemetry)
            if state["error"] or state["generations"]!=300:return 1
        return int(any(state["case_errors"] for state in states))
    finally:
        if claimed:report(client,args.out_dir,states,args.comparison_id,args.contract_telemetry)
        lock.unlink(missing_ok=True)
