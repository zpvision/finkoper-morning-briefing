# briefing-local: локальные свидетельства

Дата: 2026-11-02. CLI 1.80.0. Источник mock: postman/specs/ollama-subset.openapi.json, контролируемый fixture native Ollama endpoint, с полями официального upstream контракта. Поиск опубликованного mock владельца пропущен: облачная авторизация и workspace не использовались.

## Назначение и предпосылки

Только HTTP-зависимость Ollama, POST /api/chat. Настоящие Go handler, worker и SQLite выполняются в TestExternalSimulation. Требуется свободный loopback порт 18085. One-port routing: URL теста = http://127.0.0.1:18085/ollama. Proxy не используется, origin не требуется, passthrough к реальному Ollama отсутствует. SQLite не моделируется. Mock требует источник m1, не анализирует переписку и не является эталонной моделью.

## Воспроизведение

Из корня: `postman simulation run postman/simulations/briefing-local/briefing-local.sim.yaml --port 18085 --output ndjson`.
В другом PowerShell задать BRIEFING_TEST_SIMULATION_URL=http://127.0.0.1:18085/ollama и выполнить `go test ./internal/briefing -run TestExternalSimulation -v -count=1 -timeout 20s`; затем удалить переменную. Остановить simulation Ctrl+C. Изменения handler требуют перезапуска.

## Проверено

2026-11-02, доработка бюджета: mock расширен /api/version, /api/tags, /api/show и render-only ответом /api/chat по предоставленному шаблону. TestExternalSimulation с BRIEFING_TEST_BUDGET_PYTHON PASS: настоящий Go → subprocess Python/tokenizers → HTTP mock, задание succeeded. Отдельно cmd/budget --verify --probe подтвердил искусственную пробу и все три входа (01,02,04-exact-20) против этого mock; input counts 1848/1122/1827. Проверки runtime в этом прогоне относятся только к локальному fixture, не к серверной Ollama. Обычные tests используют явно тестовый счётчик, доп. локальные tests — настоящий helper. Метрики Ollama в mock искусственные и не используются как доказательство точности подсчёта.

2026-11-02: TestExternalSimulation PASS: HTTP 202, persisted job, worker вызвал зависимость, статус succeeded, один пункт с m1 и load_ms=0. В ndjson наблюдался request: routeKey=ollama, POST /api/chat, statusCode=200, responseSize=336. Других членов simulation нет. Это подтверждает совместимость приложения с данным fixture по реальному HTTP, не работу реального AI-сервера.

Обычные Go-тесты отдельно проверяют HTTP error, таймаут, invalid JSON, error field, length/not done, enum/источники/nullable поля, auth, idempotency race, queue recovery/capacity, shutdown и serial execution. Эти ошибки прогоняются через httptest, не Postman fault injection.

## Не проверено

Версия установленной Ollama, поддержка truncate=false, think=false и JSON schema на ней; смысловые ожидания примеров; бюджет против реального tokenizer/template; поведение GPU, загрузка модели и конкуренция RAG/тональности. Нужна отдельная ручная оценка. К серверу не подключались.
