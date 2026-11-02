# Локальные API-артефакты

`specs/briefing.openapi.json` — контракт сервиса. `specs/ollama-subset.openapi.json` — минимальный upstream fixture для используемого POST /api/chat, не полная спецификация Ollama. Mock создан из fixture локальным CLI 1.80.0. Поиск владельца в облаке пропущен: работа без авторизации Postman и подключения workspace. Никаких push/deploy.

Обычные Go-тесты используют httptest: настоящий HTTP-клиент, контролируемые ответы Ollama и временную SQLite. Отдельный opt-in тест проверяет приём задания, worker и чтение результата через Postman simulation:

```powershell
# Терминал 1; остановка Ctrl+C:
postman simulation run postman/simulations/briefing-local/briefing-local.sim.yaml --port 18085 --output ndjson
```

```powershell
# Терминал 2:
$env:BRIEFING_TEST_SIMULATION_URL='http://127.0.0.1:18085/ollama'
# Для проверки настоящего helper (нужны подготовленные локальные ресурсы и venv):
$env:BRIEFING_TEST_BUDGET_PYTHON=(Join-Path $PWD '.venv-budget/Scripts/python.exe')
go test ./internal/briefing -run '^TestExternalSimulation$' -v -count=1 -timeout 20s
Remove-Item Env:BRIEFING_TEST_SIMULATION_URL
Remove-Item Env:BRIEFING_TEST_BUDGET_PYTHON
```

В журнале должны появиться request к /api/version, /api/tags, /api/show и /api/chat. Для /api/chat сначала проверяется render-only, затем выполняется контролируемая генерация. Тест создаёт временную БД, разрешает только loopback URL, без BRIEFING_TEST_SIMULATION_URL пропускается. Без BRIEFING_TEST_BUDGET_PYTHON используется явно тестовый счётчик; с этой переменной запускается настоящий Python subprocess/tokenizers, а Ollama остаётся mock. Fixture предназначен только для интеграционных проверок, не для оценки смыслового качества примеров. Mock не моделирует GPU, конкуренцию других сервисов и реальный вывод модели.

Postman init создал локальные AGENTS.md и skills/. Облачная workspace не привязана, секретов и реальных переписок в артефактах нет.
