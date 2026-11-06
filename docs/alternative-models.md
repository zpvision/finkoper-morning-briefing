# GPT-OSS 20B и Gemma 4 26B: отдельная диагностика

tools/compare_alternatives.py — отдельный клиент для Ollama 0.40.15 через http://127.0.0.1:11435. Рабочий API и существующие диагностические профили не меняются. Go bridge cmd/summary-contract экспортирует точные константы diagnostic-summary-v2/summary-v1 и тот же сериализованный user JSON через briefing.PromptInput; он же валидирует итог через ValidateSummary. Не извлекаем промпт регулярными выражениями из исходников и не собираем его заново в Python.

Обязателен --baseline: свежий Qwen 27B JSONL. Из смешанного v2/v5 отчёта берутся только шесть v2 записей; проверяются модель, done/stop, отсутствие ошибок, валидность summary и соответствие хешей system/schema/user/исходного файла. В каждую новую запись включена соответствующая исходная строка Qwen и SHA256 файла отчёта. review.md не читается, остальные 18 входов не выбираются.

Модели/digest закреплены в tools/alternative-models.json:

* gpt-oss:20b — f38aa0c53da5f8c49d08c43a99df24ff53167fe68e24664a7777288e7656fdfe; think="low", полный резерв 4096, включая reasoning и финальный JSON.
* gemma4:26b — 001e5dafc3c77684c2307ebc6ab8e336e10c9b18eca52acf547d72fc83c3ca8c; think=false, резерв 1000.

Остальное передаётся явно как у Qwen baseline: temperature=0.7, top_p=0.8, top_k=20, min_p=0, presence_penalty=1.5, repeat_penalty=1, num_ctx=8192, stream=false, keep_alive=30m, truncate=false, shift=false. HTTP timeout=600s. В отчёте сохраняются явные параметры и model_default_parameters из runtime: серверные значения по умолчанию не выдаются за полностью известный набор эффективных опций. Сравнение меняет модель, обёртку чата, tokenizer и think; у GPT также резерв. Это не изоляция влияния одной переменной и не benchmark скорости.

## Токенизаторы и renderer

Используются отдельные официальные HF tokenizers без весов, с закреплёнными revision, SHA256 и размером файлов. При подготовке полностью сопоставлены vocab, merges и ID added tokens с verbose show; канонический хеш всех tokenizer.* закреплён и проверяется при каждом сетевом вызове. Никакого собственного BPE. CPU tokenizers==0.22.1 в отдельном venv, padding/truncation отключены. GPT normalizer=None, у Gemma сохраняется только её штатная замена пробела на U+2581; текст запроса не меняется.

Источники tokenizer:

* [OpenAI GPT-OSS 20B](https://huggingface.co/openai/gpt-oss-20b/tree/6cee5e81ee83917806bbde320786a8fb61efebee).
* [Google Gemma 4 26B-A4B IT](https://huggingface.co/google/gemma-4-26B-A4B-it/tree/4d7ae4984b7db7de8f8457170b3f1a419ee76d52).

GPT использует Harmony: обёртка system с датой/Reasoning: low, точный исходный diagnostic-summary-v2 внутри developer Instructions, затем неизменный user. Это штатная Ollama template этой модели; назвать всю обёртку идентичной Qwen нельзя. Ожидаемый текст проверяется целиком против render-only, включая серверную дату и format=schema.

Gemma требует **gemma4-large**, хотя Modelfile говорит RENDERER gemma4. [Ollama renderer_resolution.go](https://github.com/ollama/ollama/blob/02480ca2a47153e1f9c31cbe1e05536da97ce1ba/server/renderer_resolution.go) разрешает 26B в large (порог 12B); [renderer](https://github.com/ollama/ollama/blob/02480ca2a47153e1f9c31cbe1e05536da97ce1ba/model/renderers/gemma4.go) добавляет пустой thought-блок при think=false. Источники и checksum закреплены в manifest. BOS считается один раз: [llama_server.go](https://github.com/ollama/ollama/blob/02480ca2a47153e1f9c31cbe1e05536da97ce1ba/llm/llama_server.go) убирает leading BOS перед native completion, которая добавляет BOS сама.

Бюджет считается по фактически отрендеренному запросу до генерации, с полным резервом. Отсутствие render-only или отличие от изученного шаблона — остановка; обычная генерация не подменяет проверку. После ответа обязательно сравнивается prompt_eval_count с локальным счётчиком; расхождение останавливает следующие вызовы. Совпадение count подтверждает число токенов конкретного запроса, а не все возможные строки. /api/tokenize на этом сервере отвечает 404; не используем выдуманный endpoint или приватные порты runner.

## Порядок и GPU

Без --run — план без Ollama-запросов. --prepare отдельно разрешает загрузку только pinned tokenizer файлов, без весов. --stage probes --run вызывает syn-02 на GPT, затем на Gemma. Модели запускаются по одной: только известные диагностические модели выгружаются через пустой chat/keep_alive=0 (не генерация); неизвестную resident-модель клиент не трогает и прерывается. Сервисы и глобальные настройки не меняются.

Перед и после генерации проверяется /api/ps: присутствие модели, size>0 и size_vram==size. Если локальная CLI ollama есть, дополнительно выполняется OLLAMA_HOST=... ollama ps и сохраняется вывод. В текущем окружении CLI отсутствует, поэтому использован /api/ps — тот же источник данных, что у ollama ps. Это сведения Ollama о GPU-размещении, не независимое измерение nvidia-smi/резидентности всех MoE экспертов. Сырые размеры сохранены, не скрываются.

Только после обеих технически успешных GPU-проб --stage remaining --run разрешает по пять остальных syn-01, syn-03…syn-06; syn-02 не генерируется снова. Gate проверяет оба отчёта, модели, manifest snapshot, baseline, user/system/schema, done/stop, runtime count, GPU и итоговую структуру. Повторный remaining и повторный probes с существующими результатами отклоняются. Atomic lock предотвращает параллельный запуск в один каталог; после аварийного убийства lock нужно исследовать вручную, автоматического снятия/повтора нет.

Каждой модели соответствует отдельный JSONL. Сохраняются точный final_content, result, ошибки структуры/полноты, usage и длительности, бюджет/хеши/параметры, GPU before/after, reference Qwen. message.thinking не сохраняется и не логируется — только наличие и размер. Length, пустой content, невалидный JSON/ID — ошибки. Смысл по ключевым словам не оценивается.

При текущей подготовке Gemma сначала получила отказ renderer до генерации. Причина найдена в large-resolution, проверка исправлена по исходникам. Эта строка с generation_attempts=0 оставлена в JSONL. Единственная реальная Gemma syn-02 выполнена после явного --stage gemma-probe (только после такого нулевого отказа); успешная GPT проба не повторялась. Этот специальный режим не ремонтирует JSON, не повторяет генерацию и отказывает, если Gemma уже генерировала. Manifest snapshots сохраняют версии до/после уточнения renderer.

Mock-тесты проверяют транспорт, запрет повторов, порядок 2+10, бюджет, renderer, count и неполные ответы; не доказывают смыслового качества.

request_sha256 обозначает SHA256 канонического JSON payload (sort_keys=True, ensure_ascii=False, UTF-8), не побайтовый хеш HTTP framing. System/user хешируются по исходным UTF-8 строкам Go-контракта.

Фактический локальный прогон 2026-11-06: 6 генераций GPT и 6 Gemma, все 12 структурно валидны, все 12 prompt_eval_count совпали. Отчёты evaluation/reports/alternatives-20261106/gpt-oss.jsonl и gemma4.jsonl; дополнительно comparison.md и technical-summary.json. Источник Qwen: evaluation/reports/compare-v2-v5-20261106-110917/comparison.jsonl, только v2. В Gemma JSONL сохранён один предгенерационный отказ с нулём генераций. Данные Ollama ps/API показывали size_vram==size; локальная ollama CLI отсутствовала, независимого nvidia-smi измерения не было. Качество смыслов не объявляется подтверждённым. Отчёты/токенизаторы исключены из Git.
