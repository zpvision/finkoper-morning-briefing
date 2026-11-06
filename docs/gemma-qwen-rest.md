# Gemma / Qwen v2: syn-07–syn-24

Расширен существующий tools/compare_alternatives.py, явный --suite gemma-qwen-rest. Только 18 файлов syn-07.json…syn-24.json читаются Go-контрактом cmd/summary-contract; syn-01…syn-06, baseline старого набора и review.md не читаются. Source/user сериализуется через тот же briefing.PromptInput и валидируется через LoadFiles. System — точный diagnostic-summary-v2, schema — summary-v1. Хеши system/user/schema/исходных файлов сохраняются. Настройки рабочего API и глобальные настройки Ollama не меняются, сервер 193.106.173.205 не используется.

Новый отдельный manifest tools/gemma-qwen-models.json переиспользует проверенные profiles/tokenizers: Gemma4-large, Qwen27 с отключением NFC в памяти. Digests прежние, CPU tokenizers==0.22.1, полная сверка vocab/merges/added IDs и tokenizer.* с runtime. Для обеих: think=false, temperature=0.7, top_p=0.8, top_k=20, min_p=0, presence_penalty=1.5, repeat_penalty=1, num_ctx=8192, num_predict/полный резерв=1000. HTTP timeout сравнительного клиента=600s, как в прежней cross-model диагностике; отдельная baseline-команда Qwen с timeout 120s не менялась. Все фактические параметры записаны в JSONL.

Без --run строится план всех 36 сочетаний на CPU, с candidate-render и реальными закреплёнными токенизаторами, без Ollama-запросов. Проверяются input_tokens+1000<=8192, сохраняется plan.json. Флаг --run требует сохранённый успешный план с теми же 36 парами model/case, хешами входов, промпта, схемы и manifest. План не заменяет runtime preflight: перед каждым вызовом заново проверяются digest/version/checksum, точный render-only, GPU reported allocation и бюджет. После генерации обязательны done/stop, итоговый JSON, ID источников и совпадение prompt_eval_count.

Один последовательный прогон: 18 Gemma, выгрузка диагностической модели, затем 18 Qwen. Максимум 36 генераций, по одной на model/case, без repair/retry/fallback. Модели выгружаются пустым chat/keep_alive=0, службы не останавливаются; неизвестную resident-модель клиент не трогает. Источник GPU-сведений — /api/ps (при наличии локальной ollama CLI также ollama ps); это reported size_vram==size, не независимая проверка резидентности всех MoE-экспертов через nvidia-smi.

Вывод: gemma4.jsonl, qwen-v2.jsonl и comparison.md обновляются после каждого ответа. summary.json содержит числа генераций/успехов и список ошибок. Незавершённые строки comparison.md отмечены «НЕ ЗАПУЩЕНО». Если проверка несовместимости/счётчика/GPU не прошла, прогон прекращается с сохранением частичных файлов. Ошибка отдельного ответа сохраняется и не повторяется. Параллельный запуск в один каталог запрещён lock-файлом; повторный прогон в каталог с JSONL отвергается до генерации. Текст thinking не сохраняется и не логируется. Semantic score не вычисляется; review.md остаётся ручным критерием.

Команды из корня проекта через существующий туннель:

```powershell
go build -o bin/summary-contract.exe ./cmd/summary-contract
$common = @('--bridge','bin/summary-contract.exe','--input-dir','finkoper-synthetic-24/inputs','--suite','gemma-qwen-rest','--base-url','http://127.0.0.1:11435','--out-dir','evaluation/reports/gemma-qwen-rest-20261106')
& .venv-budget/Scripts/python.exe tools/compare_alternatives.py @common
# Только после успешного плана, один запуск:
& .venv-budget/Scripts/python.exe tools/compare_alternatives.py @common --run
```

Для новой ручной попытки нужен новый каталог. Не запускайте тот же тест повторно автоматически. --prepare разрешает только pinned tokenizer/source downloads, без весов и без запросов генерации. Старый first-six-alternatives и его отдельные probes/remaining сохранены.
