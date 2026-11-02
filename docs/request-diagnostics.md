# Диагностика сборки запроса и ручное сравнение A/B/C

Рабочие API, morning-v3, topics-v1, OutputSchema и Python-helper в этой доработке не изменены. Команда `diagnose` находится отдельно от HTTP-сервиса, не создаёт заданий в SQLite и не применяется автоматически. Оптимизация helper не внедрена.

## Что проверено в текущем пути

1. `briefing.PromptInput(in)` возвращает compiled SystemPrompt и string(userData(in)). Контекст и messages отдельно размечены; стабильная сортировка не меняет text. Системный промпт не читается из .env и не выбирается по имени клиента.
2. `Ollama.Generate` один раз получает эти две строки, передаёт их в checkBudget → PythonCounter.Count. JSON через stdin декодируется Python json.loads без подмены текста. ContextBudget.render добавляет только закреплённые маркеры ролей, /no_think и assistant prefix. Tokenizer.normalizer=None; текст для генерации не нормализуется.
3. Затем **те же строки** идут в PythonCounter.Verify → helper.verify. Проверяются файлы, версия 0.40.5, digest модели и хеш шаблона; полученный `_debug_info.rendered_template` сравнивается с полным ожидаемым рендерингом. Нет поля/нет совпадения → ошибка, без fallback.
4. Генерация получает messages=[{role:system,content:system},{role:user,content:user}], где system и user — те же локальные переменные. Format передаётся отдельно как json.RawMessage(OutputSchema), в системный промпт он не вставляется. В приложении нет второго системного сообщения или кода, заменяющего prompt. JSON escaping при передаче через Go/Python не равнозначен изменению декодированной строки.
5. В обычном helper render-only запросе **нет format**. Это не обнаруженная потеря промпта, но граница прежней проверки: она не проверяла рендеринг вместе с конкретным format генерации. Новая диагностическая команда дополнительно отправляет render-only с точной копией payload соответствующего варианта, включая format, и сравнивает весь rendered_template. Сам рендеринг не печатается — выводятся хеш и rendered_system_preserved.

Добавлен локальный тест ProductionRequestPathAgainstExportedTemplate: PromptInput → настоящий Python subprocess → render-only HTTP mock, который рендерит **экспортированный Go template через text/template**, → запрос генерации. Проверяются роли, точное равенство system/user на всех границах и неизменность schema. Обе существующие переписки проходят этот тест. Это проверка кода и предоставленного шаблона локально; серверные прокси, изменённый бинарник, фактический runner и внутренний запрос модели без операторского прогона здесь не наблюдались. Поэтому вывод: **локально потеря или замена system не обнаружена**, а не «серверная причина исключена».

## Аудит текущей JSON Schema

В OutputSchema отсутствуют description, title, examples, default и const. Формулировок «напиши заголовок» или `needs_context=false` нет. Поле text — просто string с minLength=1: короткий заголовок удовлетворяет схеме. Boolean needs_context допускает оба значения, не имеет default=false. Схема не кодирует смысловую связь срока, необходимость контекста, число вопросов или полноту сумм/дат.

| Поле | Ограничение | Чего оно не гарантирует |
|---|---|---|
| topics | array, object items; корень без лишних свойств | Число/полнота/границы вопросов; пустой массив разрешён |
| text | string, minLength=1 | Содержательность, сохранение сумм, дат и обещаний |
| status | enum awaiting_staff/awaiting_client/resolved/unclear | Правильный статус по переписке |
| deadline_text | string или null | Связь с нужной цепочкой и запрошенным действием |
| needs_context | boolean | Правильный выбор true/false |
| source_message_ids | array строк, minItems=1, uniqueItems=true | Соответствие источников выводу; существование ID проверяется приложением отдельно |

Буквальный порядок properties и required у темы: **text → status → deadline_text → needs_context → source_message_ids**. В исходящем Go JSON он сохранён внутри RawMessage; тест сравнивает переданный format с OutputSchema. Перестановки/добавления описаний в A не делаются. Потенциальное влияние порядка генерации полей — гипотеза: ранний text может фиксировать краткую формулировку до источников, но это не доказанная причина смысловых ошибок.

Дальше порядок зависит от серверного пути. В [адаптере Ollama 0.40.5](https://github.com/ollama/ollama/blob/v0.40.5/llm/llama_server.go) completion path передаёт schema как RawMessage в json_schema; native chat path разбирает её в map и передаёт response_format. Поэтому порядок исходного Go literal нельзя без проверки объявлять окончательным порядком декодирования на вашей сборке. В [конвертере llama.cpp b11669](https://github.com/ggml-org/llama.cpp/blob/b11669/common/json-schema-to-grammar.cpp) boolean допускает true и false; формат ограничивает допустимые продолжения, но не устанавливает смысловое значение needs_context. Сервер может иметь собственные изменения, а выбор backend/grammar здесь не наблюдался.

Также B сохраняет фразу текущего SystemPrompt «matching the supplied schema», хотя при format="json" схема отдельно **не передаётся**. Нельзя незаметно дописать её в B: это нарушило бы условие «тот же промпт». Поэтому B может дать JSON иной формы; команда сохранит его и отдельно отметит structural_validation_error. Это существенное ограничение интерпретации A/B. В коротком C форма результата кратко описана текстом; C меняет сразу длину/язык/содержание инструкции, а не изолирует только длину промпта.

## Варианты и параметры

* **A** — точный текущий SystemPrompt/morning-v3 и текущая OutputSchema.
* **B** — побайтово тот же system и user, format="json".
* **C** — отдельный диагностический ShortPrompt (diagnostic-short-v1), основные правила и форма ответа без искусственных примеров, format="json". Он не становится morning-v4 и не попадает в рабочий API.

Одинаковы model, user JSON, stream=false, think=false, keep_alive=30m, truncate=false, shift=false, temperature=0, num_ctx=8192, num_predict=1500. Нет seed, warm-up генерации, повторов, repair, эталонной подстановки или дополнительных инструкций по ID/именам/суммам. Сначала идут A/B/C для первого файла, затем A/B/C для второго. Кэш, загрузка runner и конкуренция за GPU не контролируются: длительности не являются чистым benchmark A/B/C.

На каждый вариант Count выполняется над его реальным system и тем же сериализованным user JSON; резерв и отказ при переполнении сохраняются. В --run выполняется прежний Verify плюс дополнительный format-aware render-only. Затем **ровно один вызов генерации**. До генерации ошибка останавливает команду; если ответ неполный/невалидный, он сохраняется с отметкой ошибки без повтора, а сравнение следующих вариантов продолжается. Успешный полный прогон двух файлов — 6 генераций. Render-only запросы не считаются генерациями, но они есть: по два на вариант. На сервере, игнорирующем нестандартный `_debug_render_only`, запрос может фактически сгенерировать ответ; отсутствие rendered_template вызывает остановку, не ещё одну попытку. Использовать --run только с вашей поддерживающей расширение сборкой.

## Установка и точные команды оператора

Нужны уже подготовленные ресурсы и отдельный venv из [инструкции helper](../tools/context_budget/README.md). Для сборки дополнительного Linux-бинарника локально из PowerShell:

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -o bin/diagnose-linux-amd64 ./cmd/diagnose
Remove-Item Env:GOOS, Env:GOARCH
```

Оператор вручную доставляет его как `/opt/finkoper-morning-briefing/current/diagnose` вместе с прежними файлами examples/04-exact-20.json и examples/02.json. Сервис перезапускать для диагностической команды не требуется. BRIEFING_API_TOKEN не нужен, не читается и не передаётся ни helper, ни Ollama; env-файл с токеном не требуется загружать.

**План без сети и генерации** (получится шесть JSONL-строк с хешами и локальным бюджетом):

```bash
cd /opt/finkoper-morning-briefing/current
sudo -u finkoper-briefing env \
  BRIEFING_BUDGET_PYTHON=/opt/finkoper-morning-briefing/venv-budget/bin/python \
  BRIEFING_BUDGET_HELPER=/opt/finkoper-morning-briefing/current/tools/context_budget/helper.py \
  BRIEFING_TOKENIZER_DIR=/opt/finkoper-morning-briefing/resources/tokenizer/qwen3-8b \
  BRIEFING_BUDGET_TIMEOUT=60s \
  OLLAMA_BASE_URL=http://127.0.0.1:11434 OLLAMA_MODEL=qwen3:8b OLLAMA_TIMEOUT=120s \
  ./diagnose examples/04-exact-20.json examples/02.json
```

**Реальные запросы — только явный ручной запуск с --run:**

```bash
cd /opt/finkoper-morning-briefing/current
report=$(mktemp /tmp/briefing-diagnostic-XXXXXX.jsonl)
sudo -u finkoper-briefing env \
  BRIEFING_BUDGET_PYTHON=/opt/finkoper-morning-briefing/venv-budget/bin/python \
  BRIEFING_BUDGET_HELPER=/opt/finkoper-morning-briefing/current/tools/context_budget/helper.py \
  BRIEFING_TOKENIZER_DIR=/opt/finkoper-morning-briefing/resources/tokenizer/qwen3-8b \
  BRIEFING_BUDGET_TIMEOUT=60s \
  OLLAMA_BASE_URL=http://127.0.0.1:11434 OLLAMA_MODEL=qwen3:8b OLLAMA_TIMEOUT=120s \
  ./diagnose --run examples/04-exact-20.json examples/02.json > "$report"
status=$?
printf 'report=%s exit=%s\n' "$report" "$status"
```

mktemp создаёт закрытый файл: он содержит результаты модели, потенциально данные переписок. Не публиковать его в репозитории. Команда не выводит system/user полностью и не выводит сырой upstream error, HTTP headers, env или API-токен. Невалидный JSON модели намеренно сохраняется как invalid_json_content для анализа, не как успешный result. Exit 1 возможен при структурном отличии B/C; это не повод повторять генерацию автоматически.

## Поля отчёта и чтение результатов

variant/mode/prompt_version, prompt_sha256, user_json_sha256, messages_sha256, format_sha256, request_sha256, system_bytes/user_json_bytes позволяют проверить конкретный бинарник и неизменность входов. A и B должны иметь одинаковые prompt/messages/user hashes; C — другой prompt, но тот же user hash. rendered_prompt_sha256 и rendered_system_preserved подтверждают полное совпадение format-aware rendered prompt с ожидаемым, а не просто наличие подстроки system. У A/B ожидается одинаковый rendered hash.

В budget — локальные input_tokens, резерв 1500, контекст 8192, required/remaining, метод и compatibility_verified для **текущего варианта**. В metrics — prompt_eval_count/input_tokens и eval_count/output_tokens, длительности Ollama и этапов helper. Дополнительно format_render_check_ms, ollama_http_ms, processing_ms и prompt_eval_count_minus_local (null, если сервер не вернул метрику). Разницу токенов нужно расследовать с учётом special tokens/runner/cache; команда не корректирует бюджет по ней автоматически.

result — оригинальный JSON модели, без переформулировки. structural_validation_error — независимая проверка по прежней topics-v1 и источникам; error — ошибка транспорта/неполноты/проверок. done_reason=length не принимается за успех. Даже отсутствие обоих ошибок не доказывает правильность тем и сроков.

Сначала проверить сохранение system и хеши A/B; затем сравнить разделение тем, привязку сроков, needs_context, суммы/даты/обещания и источники. Если B заметно лучше A на том же входе, это поддерживает гипотезу о влиянии schema-constrained decoding, но один прогон не устанавливает причину. Если C лучше обоих, это повод отдельно исследовать формулировку/нагрузку инструкций. Пока результатов реального сравнения нет, ни schema, ни prompt по этим гипотезам не менять.

## Отдельное предложение по повторной загрузке tokenizers — не реализовано

Минимальное следующее изменение: добавить helper-операцию `count_verify` для worker. Один subprocess создаёт ContextBudget, один раз проверяет/загружает tokenizer, считает вход, затем выполняет все прежние version/digest/template/render-only проверки тем же объектом. Перед Verify повторно проверить неизменность хешей файлов и manifest, но не создавать Tokenizer заново; при изменении — отказ, а не использование устаревшего count. Строки system/user передаются одним JSON; таймаут, ограничение stdout, отдельный venv, fail-closed и проверки на каждое задание сохраняются. Нужно сохранить отдельные внутренние замеры count и verify, а время старта процесса учитывать явно. Это уберёт вторую загрузку tokenizer и второй запуск Python внутри worker, без постоянного daemon.

Подсчёт при HTTP-приёме — ещё один отдельный запуск, не входящий в processing_ms; его объединение с worker невозможно напрямую из-за устойчивой очереди и задержки между приёмом и обработкой. Если надо убрать загрузки и между заданиями, следующий вариант сложнее: один приватный долгоживущий Python child с ограниченным JSONL-протоколом, request ID и последовательным доступом. Кэшировать только загруженный tokenizer при неизменных хешах файлов/manifest; версию/digest/шаблон и render-only продолжать проверять для каждого задания. Смена файлов инвалидирует кэш; ошибка/таймаут завершает child и отклоняет текущую операцию без обхода проверки. Ни объединённый режим, ни persistent child в данном коммите не добавлены.
