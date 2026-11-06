# Gemma: раздельные стадии и сравнение оптимизации

`tools/compare_alternatives.py --suite gemma30 --profile-gemma30` использует прежние HTTP-транспорт, Go-контракт, input validation и Gemma-токенизатор. Он выполняет current (30 последовательных), optimized (30 последовательных), затем concurrent2 (30 кейсов, максимум два одновременно), только если optimized не достиг 1500 технически успешных обзоров/час по полному времени. Ошибка совместимости прекращает дальнейшие режимы; уже запущенные concurrent2 запросы завершаются и сохраняются. Без повторов, repair и fallback. План без сети, реальный запуск только с `--run`.

Модель, digest, diagnostic-summary-v2, summary-v1, think=false, sampling и 8192/1000 не меняются. `long-01…long-30` читаются через прежний Go bridge; review.md не читается. Параметры, source/user/prompt/schema/request hashes и ответы сохраняются в отдельных JSONL для каждого режима.

В current объект `tokenizers.Tokenizer` уже создаётся один раз на партию, но каждый из двух `verify_show` на кейс читает/разбирает tokenizer.json и получает verbose metadata заново. С учётом общих граничных аудитов ожидается 62 полных проверки/JSON-парсинга, одна загрузка BPE engine. В optimized/concurrent2 разобранный словарь/merges и engine хранятся в памяти: один JSON-парсинг, два полных граничных аудита, один engine. Локальные checksum проверяются в начале и конце. Для каждого входа остаются точный runtime render, локальный бюджет с резервом, runtime prompt count, done/completeness, JSON и ID источников. До/после генерации проверяются GPU и закреплённый digest резидентной модели; убраны только дополнительные дублирующие снимки резидентности. Изменение инвариантов на завершающем аудите делает партию неуспешной.

Отдельно измеряются: checksums/import/engine load; model identity, metadata HTTP/hash/compare и JSON-load; render-only; CPU encode; GPU checks; HTTP-генерация; validation stage, Go-helper Popen/CreateProcess, work/IPC wait и внутренняя Go-валидация; сериализация/запись/fsync JSONL; обновление отчётов и непосредственная запись Markdown. Вложенные стадии не складываются повторно. Python helper не запускается отдельным процессом на каждый кейс — это сам прежний Python-клиент. Go-helper валидации по-прежнему запускается на кейс; его старт не объявляется временем работы токенизатора.

GPU перед каждым режимом подтверждается через /api/ps и render-only без дополнительной генерации. Это резидентная модель и проверенный renderer, без утверждения о прогреве inference kernels/prefix cache. Прогрев измерен отдельно и исключён из тёплого сравнения. Все остальные затраты режима (ресурсы, граничные аудиты, результаты и финальные отчёты) включены в полное время; интервал кейсов до последнего fsync показан отдельно. Общая подготовка Go-контракта вынесена отдельно. Сервисы, GPU slots и глобальные настройки Ollama не меняются; HTTP overlap не доказывает GPU-параллелизм.

В current Markdown пересоздаётся после каждого результата и в конце. В optimized/concurrent2 — в конце/при ошибке; JSONL всегда flush+fsync на ответ. Ошибки и частичные результаты сохраняются. p95 считается nearest-rank; задержка параллельного запроса включает очередь. Прогноз 3000 приведён как линейная оценка по всей 30-кейсной партии и как одна партия с однократными фиксированными затратами. Качество текста автоматически не оценивается, длины ответов и эффект prompt cache могут различаться при неизменном stochastic sampling.

```powershell
go build -o bin/summary-contract.exe ./cmd/summary-contract
if ($LASTEXITCODE -ne 0) { throw 'build failed' }
$profileArgs = @('--bridge','bin/summary-contract.exe','--input-dir','finkoper-gemma-30/inputs','--suite','gemma30','--profile-gemma30','--out-dir','evaluation/reports/gemma30-profile-20261106')
& .venv-budget/Scripts/python.exe tools/compare_alternatives.py @profileArgs
if ($LASTEXITCODE -ne 0) { throw 'plan failed' }
# Один запуск; максимум 90 генераций, concurrent2 только по измеренному порогу:
& .venv-budget/Scripts/python.exe tools/compare_alternatives.py @profileArgs --run
Write-Output "Exit: $LASTEXITCODE"
```

Итог — `profile.md`, `profile.json`; внутри current/optimized/concurrent2 — `gemma4.jsonl`, `phase.json`, `comparison.md` и `benchmark.md` (последний сохраняет прежнюю метрику «от первого кейса до последнего fsync»; полное профилируемое время берётся из profile.md/phase.json). В измеренное время режима входят его финальные Markdown-отчёты; агрегаты phase.json и общий profile.md/profile.json записываются после остановки таймера. Отчёты исключены из Git. Каталог с уже начатыми режимами не перезаписывается, автоматического возобновления с повторными генерациями нет.

Проверки: `go test ./...`, `go vet ./...`, сборки Windows/Linux и `.venv-budget/Scripts/python.exe -m unittest tools/test_compare_alternatives.py tools/test_profile_gemma30.py`. Mock подтверждает сохранение input guards при кэшировании, максимум двух активных задач, отсутствие повторов, остановку/сохранение in-flight при несовместимости и условный запуск concurrent2. Смысловая точность и скорость реальной модели этим не доказываются.
