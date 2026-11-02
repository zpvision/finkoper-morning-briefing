# Двухэтапная диагностика: группировка → topics-v1

Это отдельный эксперимент `cmd/diagnose-two-stage`, а не замена рабочего morning-v3. HTTP API, очередь, OutputSchema/topics-v1 и Python-helper сервиса не меняются. По сообщению оператора реальный A/B/C не дал приемлемого смысла, хотя system сохранялся и локальный count совпал с prompt_eval_count во всех шести вызовах. Это основание попробовать разбиение задачи, но не доказательство качества нового режима.

## Этапы

**GROUP**, промпт diagnostic-group-v1, выход issue-groups-v1: один вызов на весь набор клиента, без красивой сводки, итоговых статусов и отдельного вызова на вопрос. Вход — тот же канонический original JSON с отдельно размеченными messages и context_messages.

Выход — объект issues с массивом групп:

| Поле группы | Смысл |
|---|---|
| message_ids | Непустой массив ID относящихся сообщений периода |
| context_message_ids | ID объясняющего контекста; [] при отсутствии |
| request | Кратко извлечённая просьба/предмет вопроса, не итоговая сводка |
| deadline_message_id | ID сообщения с явно запрошенным сроком по этому вопросу либо null |
| staff_reply_ids | ID ответов сотрудника, включая обещания без признания выполнения |
| resolution_confirmation_ids | ID сообщений, явно подтверждающих выполнение/получение запрошенного результата/снятие вопроса |
| missing_information | Массив описаний того, чего не хватает для понимания; [] если не выявлено |

Все поля обязательны, лишние запрещены. Проверяется JSON, типы и непустой request, разделение ID периода/контекста, уникальность в каждом массиве, существование ссылок, принадлежность специализированных ссылок группе и роль staff у staff_reply_ids. Разные группы могут ссылаться на одно сообщение, если оно касается нескольких вопросов. Пустой issues допускается: даже он не считается доказательством отсутствия вопросов. Структурная проверка не доказывает правильность групп, дедлайнов и подтверждений; возвращается первый найденный структурный код ошибки, не список смысловых ошибок.

**FINAL**, промпт diagnostic-final-v1, выход — прежние topics-v1: один вызов на весь набор клиента. Вход:

* original_input — **все исходные сообщения**, не выборка по группам; канонический объект периода/клиента/контекста сохранён;
* grouping_candidate — исходный результат первой модели, версия схемы, ошибки структуры и `trusted=false`. Если JSON первой модели невалиден, передаётся его строка invalid_json_content, а output=null.

Второй промпт явно требует перепроверить источники, добавить пропущенное, исправить разделение/сроки/ссылки/недостающий контекст, а не принять группировку за истину. Код не исправляет промежуточный результат и не подставляет эталон. Если grouping структурно ошибочен, но ответ модели полный (done=true, done_reason=stop), FINAL всё равно выполняется как предусмотренный второй этап. Ошибка остаётся видимой в отчёте, даже если итог структурно корректен. При неполном ответе GROUP, HTTP/transport/compatibility ошибке или переполнении его бюджета FINAL не запускается. Это не бесконечный repair и не дополнительный вызов исправления: всего максимум два вызова генерации на клиента.

Финальный результат независимо проверяется прежним ValidateResult по topics-v1, enum и источникам исходного задания. Ни имена, ни суммы, ни ID, ни готовые ответы двух клиентов не зашиты в промпты. В коде нет правил, заменяющих результат модели эталонными темами.

## Бюджет и совместимость

Каждый этап отдельно считает **свой фактический system + user JSON** через существующий Python/tokenizers helper. Для обоих num_ctx=8192 и резерв num_predict=1500. Для FINAL считается полностью augmented JSON: original_input + вся grouping_candidate + ошибки/метаданные. Нет сложения приблизительных длин, отбора только удобных источников, обрезки группировки или текста. Если второй вход не помещается, GROUP сохраняется в отчёте, FINAL получает input_budget_exceeded и generation_attempts=0; нового вызова/repair нет.

Для каждого выполненного этапа сохранены прежние checksum/version/digest/template/render-only проверки helper и дополнительная проверка точного format-aware рендеринга из команды A/B/C. Ровно system+user, think=false, stream=false, keep_alive=30m, temperature=0, truncate=false, shift=false. Render-only запросы не являются запланированными генерациями, но могут загрузить Ollama runner. Использовать с поддерживающей `_debug_render_only` сборкой: сервер, игнорирующий расширение, может вместо него сгенерировать ответ; отсутствие rendered_template вызывает отказ без повторной попытки.

## Команды оператора

Предполагаются подготовленные ресурсы и отдельный venv из [инструкции helper](../tools/context_budget/README.md). Из локального PowerShell собрать дополнительный бинарник:

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -o bin/diagnose-two-stage-linux-amd64 ./cmd/diagnose-two-stage
Remove-Item Env:GOOS, Env:GOARCH
```

Вручную доставить его как `/opt/finkoper-morning-briefing/current/diagnose-two-stage` вместе с существующими examples/04-exact-20.json и examples/02.json. Рабочий сервис не нужно заменять/перезапускать. API-токен не нужен и не читается.

**План без сети/генерации** (считает GROUP для обоих файлов):

```bash
cd /opt/finkoper-morning-briefing/current
sudo -u finkoper-briefing env \
  BRIEFING_BUDGET_PYTHON=/opt/finkoper-morning-briefing/venv-budget/bin/python \
  BRIEFING_BUDGET_HELPER=/opt/finkoper-morning-briefing/current/tools/context_budget/helper.py \
  BRIEFING_TOKENIZER_DIR=/opt/finkoper-morning-briefing/resources/tokenizer/qwen3-8b \
  BRIEFING_BUDGET_TIMEOUT=60s \
  OLLAMA_BASE_URL=http://127.0.0.1:11434 OLLAMA_MODEL=qwen3:8b OLLAMA_TIMEOUT=120s \
  ./diagnose-two-stage examples/04-exact-20.json examples/02.json
```

В режиме plan FINAL=null с причиной: его фактический бюджет ещё неизвестен без реальной группировки. Команда не подставляет пустую или эталонную группировку ради фиктивного подсчёта второго этапа.

**Реальные запросы — только вручную с --run**, четыре генерации на успешный прогон двух файлов:

```bash
cd /opt/finkoper-morning-briefing/current
report=$(mktemp /tmp/briefing-two-stage-XXXXXX.jsonl)
sudo -u finkoper-briefing env \
  BRIEFING_BUDGET_PYTHON=/opt/finkoper-morning-briefing/venv-budget/bin/python \
  BRIEFING_BUDGET_HELPER=/opt/finkoper-morning-briefing/current/tools/context_budget/helper.py \
  BRIEFING_TOKENIZER_DIR=/opt/finkoper-morning-briefing/resources/tokenizer/qwen3-8b \
  BRIEFING_BUDGET_TIMEOUT=60s \
  OLLAMA_BASE_URL=http://127.0.0.1:11434 OLLAMA_MODEL=qwen3:8b OLLAMA_TIMEOUT=120s \
  ./diagnose-two-stage --run examples/04-exact-20.json examples/02.json > "$report"
status=$?
printf 'report=%s exit=%s\n' "$report" "$status"
```

Выход — одна JSONL-строка на входной файл. По умолчанию используются эти же два файла, если пути не заданы. До сетевых запросов проверяются все входы. При ошибке предварительной проверки выполнение прекращается; при структурно плохом полном GROUP выполняется единственный FINAL, после чего следующие клиенты могут быть обработаны без повторения предыдущего. Exit 1 сохраняет ошибки даже при корректном FINAL; не следует автоматически повторять весь прогон ради exit 0.

## Что читать в отчёте

* grouping.result — промежуточный разбор; grouping.structural_validation_error/error — его ошибки;
* final.result — итоговые topics-v1; final.structural_validation_error/error — независимые ошибки итоговой структуры/источников;
* у каждого этапа — хеши system/user/format/request/render, rendered_system_preserved, отдельный budget, реальные input/output tokens и длительности helper/compatibility/render/HTTP/Ollama;
* generation_attempts — фактически начатые генерации всего (максимум 2 на файл); final_skipped_reason объясняет пропуск;
* total_processing_ms — wall-clock время двухэтапного pipeline, включая оба budget/compatibility этапа и сборку второго входа, без чтения файла и записи отчёта;
* total_ollama_ms, total_input_tokens, total_output_tokens — суммы доступных метрик **начатых** генераций. Если какая-то метрика начатого вызова отсутствует, её сумма null. Пропущенный FINAL не притворяется выполненным нулевым вызовом; при одном завершённом GROUP сумма его известных метрик доступна. Если генераций не было, суммы null.

Ошибочные JSON-ответы намеренно остаются в диагностическом отчёте, не объявляются успешными. Файл содержит данные модели/переписок; mktemp создаёт закрытый файл. Не публиковать отчёт в Git, не размещать в публичных логах. Заголовки авторизации и backend API-токен не выводятся.

Смысловую точность обоих этапов проверять вручную по прежним критериям двух примеров. Подсчёт, формат и наличие двух вызовов не доказывают правильность групп/сроков/needs_context. До операторского прогона качество настоящей Ollama не оценивалось.

## Локальные проверки

Обычные Go-тесты используют HTTP mock и stand-in счётчик; отдельный прогон с BRIEFING_TEST_BUDGET_PYTHON использует настоящий Python/tokenizers, не реальный AI-сервер. Проверяются: два вызова независимо от числа групп, полный original_input и неизменённая grouping_candidate в FINAL, ID/роли/типы, ошибки обоих этапов, отсутствие повторов, отдельный отказ бюджета каждого этапа и неполный ответ GROUP. Реальный локальный tokenizer также проверяет отказ для большого augmented FINAL до сетевого обращения. Старый A/B/C режим продолжает проверяться после выделения общего диагностического транспорта. Рабочий Generate сервиса не изменён.
