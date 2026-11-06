# Внешние JSON: finkoper-synthetic-24

Существующий diagnose-qwen35 принимает --input-dir только с профилем qwen35-27b-summary-v2-v1 и режимом baseline. Промпт diagnostic-summary-v2, схема summary-v1, sampling, модель/digest, Ollama 0.40.15, context 8192, резерв 1000 и timeout 120s сохранены. Проверки checksum, точного render-only, runtime count, полноты ответа, JSON и источников обязательны. Реальные вызовы только с --run; один вызов на файл, последовательно, без повторов/repair/fallback. Несовместимость останавливает прогон.

По умолчанию с --input-dir выбираются ровно syn-01.json…syn-06.json (--case first-six). --case remaining выбирает остальные JSON по имени в порядке сортировки, максимум 18. Его --run требует явного --reviewed-first после ручного просмотра ответов. Можно выбрать один stem, например --case syn-14. Старый встроенный запуск 27B sr-06/remaining и его --probe-report не меняются; внешние входы используют отдельный допуск --reviewed-first. Профили 9B и рабочий API не переключаются.

Перечисляются только обычные *.json непосредственно в inputs, без вложенных каталогов; символические ссылки и дубли имён без учёта регистра отклоняются. Только выбранные файлы читаются и проходят существующий LoadFiles: размер, строгая схема входа, даты, роли, каналы, уникальность ID, сортировка времени. Все выбранные входы проверяются до HTTP. review.md и README не читаются и модели не передаются; эталоны не меняются. Результат обозначен synthetic_external, а не независимым holdout. Источники проверяются структурно; смысловую точность оценивает оператор по review.md.

JSONL сохраняет исходный абсолютный путь file, case_id из имени файла, input_file_sha256, профиль/model/digest, параметры/версии/хеши, бюджет, итог/ошибки, токены и длительности. Thinking не сохраняется. --out не перезаписывает существующий файл. Материалы finkoper-synthetic-24 остаются внешними, не добавляются автоматически в Git.

PowerShell, из корня проекта, с уже действующим SSH-туннелем:

```powershell
$env:OLLAMA_BASE_URL = 'http://127.0.0.1:11435' # либо ваш текущий локальный порт
$env:OLLAMA_MODEL = 'qwen3.5:27b-q4_K_M'
$env:BRIEFING_BUDGET_PYTHON = (Resolve-Path .venv-budget/Scripts/python.exe).Path
go build -o bin/diagnose-qwen35.exe ./cmd/diagnose-qwen35
$inputs = (Resolve-Path finkoper-synthetic-24/inputs).Path
& ./bin/diagnose-qwen35.exe --profile qwen35-27b-summary-v2-v1 --input-dir $inputs --case first-six --out first-plan.jsonl
& ./bin/diagnose-qwen35.exe --profile qwen35-27b-summary-v2-v1 --input-dir $inputs --case first-six --check-profile --out first-check.jsonl
# Только вручную: 6 генераций.
& ./bin/diagnose-qwen35.exe --profile qwen35-27b-summary-v2-v1 --input-dir $inputs --case first-six --run --out first-results.jsonl
# После просмотра ответов и review.md, отдельный запуск:
& ./bin/diagnose-qwen35.exe --profile qwen35-27b-summary-v2-v1 --input-dir $inputs --case remaining --out remaining-plan.jsonl
& ./bin/diagnose-qwen35.exe --profile qwen35-27b-summary-v2-v1 --input-dir $inputs --case remaining --reviewed-first --run --out remaining-results.jsonl
```

Для отсутствующих локальных ресурсов 27B используйте prepare_qwen35_27.py с экспортами qwen27-show.json, ollama-tags.json, ollama-version.json и --source-dir resources/tokenizer/qwen35-9b (либо явным --download только tokenizer/source, без весов), --output resources/tokenizer/qwen35-27b. Ресурсы и отдельный manifest закреплены в профиле; использовать manifest 9B вместо 27B нельзя. Подключение туннеля, реальный renderer/count, размещение модели в GPU и качество локальные mock-тесты не подтверждают.
