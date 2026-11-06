# Сравнение diagnostic-summary-v2 и diagnostic-summary-v5

V2 сохранена побайтно. Новая V5 состоит из исходной V2 и уточнения двух правил: сохранять точное название/вид запрошенного документа, не заменяя другим; различать обещание, фактическое начало и завершение действия. Тестовых имён, ID, кейсов и готовых ответов в добавленном тексте нет. Связный русский пересказ и схема summary-v1 сохранены. V3/V4, значения по умолчанию, рабочие API и профили/ресурсы не переключаются.

В существующем diagnose-qwen35 добавлен --prompt v2|v5|compare-v2-v5 (по умолчанию v2). V5 и сравнение разрешены только с профилем 27B baseline, --input-dir и --case first-six (или отсутствующим --case, который для внешнего каталога означает first-six). Остальные 18 входов с новым промптом в этой задаче не запускаются. --prompt compare-v2-v5 --case remaining отклоняется до чтения выбранных JSON/сетевых запросов.

Порядок: v2, v5 на syn-01, затем v2, v5 на syn-02 и так до syn-06. Максимум 12 последовательных генераций за запуск, по одной на версию/вход. Без --run — 12 записей локального плана без сети; --check-profile проверяет оба точных шаблона без генераций. Нет повторов, repair, fallback или программного исправления смысла.

User JSON, schema и sampling одинаковы; различаются только system prompt и его версия/хеш. Параметры прежние: think=false, temperature=0.7, top_p=0.8, top_k=20, min_p=0, presence_penalty=1.5, repeat_penalty=1, num_ctx=8192, num_predict/резерв=1000, timeout=120s. Для каждой версии отдельно проверяются бюджет, checksum/digest/version, точный format-aware runtime renderer, prompt_eval_count, полнота, JSON и источники. Ошибка генерации сохраняется без повтора, несовместимость останавливает прогон. Thinking не попадает в отчёт.

JSONL содержит case_id, prompt_version/prompt_sha256, хеши user/schema/запроса/файла/конфигурации, параметры, бюджет, результат, ошибки, токены и длительности каждой версии. Наличие источников не доказывает смысловую полноту. review.md остаётся только для ручной оценки; не читается и модели не передаётся. Mock доказывает инструмент, не качество модели.

PowerShell с уже работающим туннелем (порт задайте через OLLAMA_BASE_URL):

```powershell
$env:OLLAMA_BASE_URL = 'http://127.0.0.1:11435'
$env:OLLAMA_MODEL = 'qwen3.5:27b-q4_K_M'
$env:BRIEFING_BUDGET_PYTHON = (Resolve-Path .venv-budget/Scripts/python.exe).Path
go build -o bin/diagnose-qwen35.exe ./cmd/diagnose-qwen35
$inputDir = (Resolve-Path finkoper-synthetic-24/inputs).Path
$common = @('--profile','qwen35-27b-summary-v2-v1','--mode','baseline','--input-dir',$inputDir,'--case','first-six','--prompt','compare-v2-v5')
& ./bin/diagnose-qwen35.exe @common --out comparison-plan.jsonl
& ./bin/diagnose-qwen35.exe @common --check-profile --out comparison-check.jsonl
# Только вручную: максимум 12 генераций, без оставшихся 18 входов.
& ./bin/diagnose-qwen35.exe @common --run --out comparison-results.jsonl
```
