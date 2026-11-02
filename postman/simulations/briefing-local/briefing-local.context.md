# briefing-local: локальные свидетельства

Дата: 2026-11-02. CLI 1.80.0. Источник mock: postman/specs/ollama-subset.openapi.json, контролируемый fixture native Ollama endpoint, с полями официального upstream контракта. Поиск опубликованного mock владельца пропущен: облачная авторизация и workspace не использовались.

## Назначение и предпосылки

Только HTTP-зависимость Ollama, POST /api/chat. Настоящие Go handler, worker и SQLite выполняются в TestExternalSimulation. Требуется свободный loopback порт 18085. One-port routing: URL теста = http://127.0.0.1:18085/ollama. Proxy не используется, origin не требуется, passthrough к реальному Ollama отсутствует. SQLite не моделируется. Mock требует источник m1, не анализирует переписку и не является эталонной моделью.

## Воспроизведение

Из корня: `postman simulation run postman/simulations/briefing-local/briefing-local.sim.yaml --port 18085 --output ndjson`.
В другом PowerShell задать BRIEFING_TEST_SIMULATION_URL=http://127.0.0.1:18085/ollama и выполнить `go test ./internal/briefing -run TestExternalSimulation -v -count=1 -timeout 20s`; затем удалить переменную. Остановить simulation Ctrl+C. Изменения handler требуют перезапуска.

## Проверено

2026-11-02, two-stage-v1: TestTwoStageActualHelperLocalSimulation и готовый бинарник diagnose-two-stage --run прошли для 04-exact-20/02 на локальном mock с настоящим Python/tokenizers. На файл ровно GROUP=1 и FINAL=1 генерации; оба system сохранены. Локальные counts с fixture-группировкой: 2009→2167 и 1304→1462. В smoke total_processing_ms=3444/3344. Mock выбирает группировочную форму по schema.properties.issues, FINAL читает original_input; он не моделирует смысловое качество. Финальные counts зависят от фактического результата GROUP и на реальном сервере будут другими. Рабочие API/morning-v3 не изменены, реальный сервер не вызывался.

2026-11-02, отдельная диагностика A/B/C: TestDiagnosticWithActualHelperLocalSimulation и бинарник diagnose --run прошли на двух файлах (04-exact-20,02) через локальный mock с настоящим Python-helper. У каждого из шести вариантов generation_attempts=1, rendered_system_preserved=true. Кроме прежнего helper Verify команда делает render-only с тем же format, что у генерации. A/B имеют одинаковые prompt/messages/user hashes; у C меняется только диагностический system и format=json. Это проверка сборки/обмена/числа вызовов, не результат сравнения смыслового качества настоящей модели. Рабочие prompt/schema/API/helper не менялись.

2026-11-02, morning-v3: TestExternalSimulation с настоящим Python-helper PASS. Persisted job metrics и лог: processing_ms=2168, budget_helper_ms=792, compatibility_check_ms=1353. Проверки metadata/render-only остались активны, выполнена одна контролируемая генерация. Это локальный mock-прогон, не измерение серверной Ollama и не проверка смыслового качества нового промпта. Локальные входные counts нового промпта: 2391/1665/2370 для 01/02/04-exact-20; все помещаются с резервом 1500.

2026-11-02, доработка бюджета: mock расширен /api/version, /api/tags, /api/show и render-only ответом /api/chat по предоставленному шаблону. TestExternalSimulation с BRIEFING_TEST_BUDGET_PYTHON PASS: настоящий Go → subprocess Python/tokenizers → HTTP mock, задание succeeded. Отдельно cmd/budget --verify --probe подтвердил искусственную пробу и все три входа (01,02,04-exact-20) против этого mock; input counts 1848/1122/1827. Проверки runtime в этом прогоне относятся только к локальному fixture, не к серверной Ollama. Обычные tests используют явно тестовый счётчик, доп. локальные tests — настоящий helper. Метрики Ollama в mock искусственные и не используются как доказательство точности подсчёта.

2026-11-02: TestExternalSimulation PASS: HTTP 202, persisted job, worker вызвал зависимость, статус succeeded, один пункт с m1 и load_ms=0. В ndjson наблюдался request: routeKey=ollama, POST /api/chat, statusCode=200, responseSize=336. Других членов simulation нет. Это подтверждает совместимость приложения с данным fixture по реальному HTTP, не работу реального AI-сервера.

Обычные Go-тесты отдельно проверяют HTTP error, таймаут, invalid JSON, error field, length/not done, enum/источники/nullable поля, auth, idempotency race, queue recovery/capacity, shutdown и serial execution. Эти ошибки прогоняются через httptest, не Postman fault injection.

## Не проверено

Версия установленной Ollama, поддержка truncate=false, think=false и JSON schema на ней; смысловые ожидания примеров; бюджет против реального tokenizer/template; поведение GPU, загрузка модели и конкуренция RAG/тональности. Нужна отдельная ручная оценка. К серверу не подключались.
