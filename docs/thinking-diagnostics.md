# Диагностика think=true

Отдельная команда `cmd/diagnose-thinking`: фиксированные входы `examples/04-exact-20.json` и `examples/02.json`, максимум две последовательные генерации, один этап на клиента. Использует прежние диагностические транспорт/helper и непосредственно SystemPrompt morning-v3 / OutputSchema topics-v1 варианта A. Рабочий API не меняется. Нет повторов, repair, обрезки и вызовов на каждый вопрос.

Параметры: qwen3:8b, think=true, stream=false, keep_alive=30m, num_ctx=8192, num_predict=4096, truncate=false, shift=false, timeout HTTP-вызова 180s независимо от OLLAMA_TIMEOUT. Резерв 4096 включает рассуждение и финальный JSON.

Sampling из [официальной карточки Qwen3-8B](https://huggingface.co/Qwen/Qwen3-8B#best-practices): temperature=0.6, top_p=0.95, top_k=20, min_p=0. Сравнение с A меняет также sampling (ранее temperature=0), резерв (1500→4096) и timeout, поэтому не изолирует только think. Это указано в каждом отчёте.

Helper по умолчанию сохраняет think=false и резерв 1500; только эксперимент включает `--thinking`. Рендеринг из исходного context_budget.py: system+user, суффикс ` /think`, assistant prefix без пустого блока think. Тот же system и сериализованный user JSON уходят в Ollama. Нормализация, truncation и padding отключены. Нет весов, GPU и скачивания ресурсов при задании.

До генерации проверяются контрольные суммы, digest модели, Ollama 0.40.5, хеш шаблона и точное совпадение `rendered_template` через `_debug_render_only` с think=true и format=schema. Транспорт дополнительно проверяет точный payload. Отсутствие возможности, несовпадение или превышение бюджета останавливают команду до генерации данного примера. Нет fallback. Если сервер проигнорирует нестандартный флаг, отсутствие поля будет ошибкой; отменить уже выполненную им работу команда не может.

Локально проверены реальные tokenizers и исполнение экспортированного Go-шаблона: 04-exact-20 — 2364 входных токена (6460 с резервом), 02 — 1659 (5755 с резервом). Это не подтверждение фактической серверной Ollama: проверка thinking-render выполняется при ручном запуске. Равенство с prompt_eval_count также требует операторского прогона.

## Локальный запуск

После [подготовки отдельного venv и ресурсов](../tools/context_budget/README.md), из корня проекта:

```powershell
$env:BRIEFING_BUDGET_PYTHON=(Join-Path $PWD '.venv-budget/Scripts/python.exe')
$env:OLLAMA_MODEL='qwen3:8b'
go build -o bin/diagnose-thinking.exe ./cmd/diagnose-thinking
./bin/diagnose-thinking.exe
```

Без `--run` — только offline plan, две строки, ноль сетевых запросов/генераций, совместимость не подтверждена. Дополнительные входы команда не принимает. В GoLand: Package `finkoper-morning-briefing/cmd/diagnose-thinking`, Working directory — корень проекта, переменные выше, Program arguments пустые для плана. API-токен не нужен.

Сборка отдельного Linux-бинарника, без загрузки на сервер:

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -o bin/diagnose-thinking ./cmd/diagnose-thinking
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

## Только ручной запуск оператором на сервере

В release должны находиться диагностический бинарник `diagnose-thinking`, обновлённый helper и два примера. Используется отдельное venv счётчика. Права чтения/исполнения нужны пользователю finkoper-briefing. API `/opt/finkoper-morning-briefing/current/briefing`, его БД `/var/lib/finkoper-morning-briefing/briefing.db` и `/etc/finkoper-morning-briefing.env` не используются. Установка/перезапуск systemd не нужны.

Сначала план без сети:

```bash
cd /opt/finkoper-morning-briefing/current
sudo -u finkoper-briefing env \
  BRIEFING_BUDGET_PYTHON=/opt/finkoper-morning-briefing/venv-budget/bin/python \
  BRIEFING_BUDGET_HELPER="$PWD/tools/context_budget/helper.py" \
  BRIEFING_TOKENIZER_DIR=/opt/finkoper-morning-briefing/resources/tokenizer/qwen3-8b \
  BRIEFING_BUDGET_TIMEOUT=60s \
  OLLAMA_BASE_URL=http://127.0.0.1:11434 OLLAMA_MODEL=qwen3:8b \
  ./diagnose-thinking
```

Затем отдельный ручной прогон, максимум две генерации:

```bash
cd /opt/finkoper-morning-briefing/current
umask 077
report=$(mktemp /tmp/diagnostic-thinking.XXXXXX.jsonl)
sudo -u finkoper-briefing env \
  BRIEFING_BUDGET_PYTHON=/opt/finkoper-morning-briefing/venv-budget/bin/python \
  BRIEFING_BUDGET_HELPER="$PWD/tools/context_budget/helper.py" \
  BRIEFING_TOKENIZER_DIR=/opt/finkoper-morning-briefing/resources/tokenizer/qwen3-8b \
  BRIEFING_BUDGET_TIMEOUT=60s \
  OLLAMA_BASE_URL=http://127.0.0.1:11434 OLLAMA_MODEL=qwen3:8b \
  ./diagnose-thinking --run > "$report"
status=$?
printf 'report=%s exit=%s\n' "$report" "$status"
```

Никаких изменений Ollama, Nginx, firewall и тональности. Ollama остаётся на loopback. Эксперимент не управляет конкурирующими запросами RAG/тональности.

## Отчёт

JSONL содержит версию и хеш промпта/schema/user/request, параметры, подтверждение рендера и его хеш, локальный budget, prompt_eval_count (`metrics.input_tokens`), eval_count (`metrics.output_tokens`), разность серверного и локального counts. `generation_attempts` — 0 или 1 на строку; сумма двух строк — всего генераций.

Длительности helper, проверки совместимости, дополнительного format-render, HTTP, Ollama и всего pipeline выводятся отдельно. Отсутствующие метрики остаются null.

Текст message.thinking никогда не сохраняется и не печатается: только thinking_present и thinking_bytes (UTF-8, не токены). JSON берётся только из message.content. Невалидный content не выводится. Result публикуется после независимой проверки структуры, статусов и источников при полном завершении. Ошибки — безопасные коды без токенов/ответов сервера.

Length, незавершённый ответ и пустой content — incomplete, result=null, exit 1. generation_limit_reached отражает done_reason=length. Ошибки структуры также дают result=null. Eval_count включает thinking и final; их отдельные токены не выдумываются. Mock и структурная проверка не доказывают смысловое качество: его оператор оценивает вручную. Отчёт содержит итоговые данные переписки, не публикуйте его в Git или общих логах.
