# Один Go-helper на 300 заданий: повтор синтетических входов

`--suite gemma30 --repeat-helper300` сравнивает два варианта при двух одновременных заданиях: `per_job_helper` — concurrent2 из f6e49b2 с запуском Go-валидатора на ответ; `persistent_helper` — тот же транспорт и проверки с одним Go-процессом `summary-contract --serve` на партию. Каждый вариант выполняет 300 заданий: десять циклов long-01…long-30. **Это повтор 30 синтетических входов, не 300 независимых клиентов.** Всего максимум 600 генераций, без повторных попыток, repair, fallback и автоматического перезапуска helper.

Job ID включает comparison UUID, режим и порядковый номер и уникален для всех 600 заданий. Он остаётся в диагностическом JSONL/IPC и не добавляется в model user JSON. Исходные сообщения, их ID, serialized user JSON, diagnostic-summary-v2, summary-v1 и параметры Gemma сохраняются. Словарь/merges и BPE engine загружаются один раз на партию; полные version/digest/template/tokenizer/checksum проверки выполняются на границах. Для каждого из 600 заданий остаются точный runtime render, бюджет 8192 с полным резервом 1000, GPU/resident digest до/после генерации, prompt_eval_count, done/completeness, JSON и источники.

Постоянный helper использует только UTF-8 JSONL через stdin/stdout, без shell и интерполяции переписки. Запрос: `{request_id, request:{Action,RequestID,Input,Content}}`; оба ID должны совпасть. Ответ: `{request_id,process_id,result,protocol_error}`. Внутренний result повторяет ID/PID и содержит прежний результат валидации и её время. ID нельзя использовать повторно. Допустимы только readiness ping и validate. Helper хранит ограниченное множество использованных ID (100000), ограничивает JSONL-frame примерно 8 MiB и не логирует содержимое. Для benchmark отправляется один ping и 300 отдельных validate; готовые ответы и результаты валидации не кэшируются.

Клиент держит таблицу ожидающих запросов по ID, отдельные reader/writer threads и ограниченную очередь; две проверки могут ожидаться одновременно. Popen/CreateProcess выполняется один раз, затем обмен через трубы. Проверяется PID процесса; неверный/неизвестный ID, неверный PID, неожиданный EOF, размер/JSON frame, таймаут 30s и ошибка транспорта прекращают helper без respawn. Таймаут охватывает ожидание записи и ответа; при зависшей трубе процесс завершается. Уже начатые модельные запросы дожидаются сохранения; новые после обнаруженной несовместимости не запускаются. В одноразовом варианте тоже проверяется возвращённый RequestID. Совместимые старые one-shot вызовы без RequestID сохранены.

GPU перед каждым вариантом прогревается прежним render-only без дополнительной генерации. Прогрев измерен отдельно, вне полного тёплого времени. Полное время = фиксированная подготовка (ресурсы/первый аудит/создание helper) + интервал заданий до fsync последнего JSONL + финальные проверки/закрытие helper/Markdown. Фиксированные стадии показаны отдельно, p95 — nearest rank по задержке до записи ответа, включая очередь. Общая Go-подготовка входов и запись агрегатов phase.json/benchmark.json выполняются вне партий и явно указаны в отчёте. Настройки Ollama, её slots, рабочий API и другие сервисы не меняются.

Отчёт показывает фактические вызовы, ошибки и ID/PID, cache hits локальных инвариантов, число загрузок engine/JSON и hashes повторов. Числовые/boolean cache-поля Ollama сохраняются только если фактически вернулись. При их отсутствии prompt_eval_duration по циклам — только косвенное наблюдение, не доказательство KV cache hit. Кэш сервера не очищается: порядок режимов, повтор текстов, прогрев, sampling и разная длина ответов ограничивают переносимость результата на новую реальную почту. Смысловое качество автоматически не оценивается.

```powershell
go build -o bin/summary-contract.exe ./cmd/summary-contract
if ($LASTEXITCODE -ne 0) { throw 'build failed' }
$repeatArgs=@('--bridge','bin/summary-contract.exe','--input-dir','finkoper-gemma-30/inputs','--suite','gemma30','--repeat-helper300','--out-dir','evaluation/reports/gemma-helper300-20261106')
& .venv-budget/Scripts/python.exe tools/compare_alternatives.py @repeatArgs
if ($LASTEXITCODE -ne 0) { throw 'plan failed' }
# Один реальный запуск, два варианта по 300 заданий; не повторять автоматически:
& .venv-budget/Scripts/python.exe tools/compare_alternatives.py @repeatArgs --run
Write-Output "Exit: $LASTEXITCODE"
```

Вывод: benchmark.md/benchmark.json и по каталогу на режим с gemma4.jsonl, results.md, progress.json и phase.json. Частичный JSONL flush+fsync на каждый ответ; прогресс/Markdown обновляются каждые 10 результатов, при ошибке и в конце. Ошибка отдельной генерации сохраняется, обработка продолжается без повторов. Несовместимость/нарушение helper-протокола останавливает дальнейший прогон. Начатый каталог не перезаписывается. Отчёты исключены из Git.

Проверки: `go test ./...`, `go vet ./...`, сборки Windows/Linux, `.venv-budget/Scripts/python.exe -m unittest tools/test_compare_alternatives.py tools/test_profile_gemma30.py tools/test_persistent_bridge.py`. Go-тесты проверяют framing, IDs/PID, дубликаты, limits и валидацию каждого сообщения. Реальные локальные subprocess-fixtures проверяют параллельную корреляцию, UTF-8, таймаут/EOF/неверный ID и отсутствие respawn; проверки модели остаются прежними HTTP mock tests. Эти тесты проверяют инструмент, не смысловую точность модели.
