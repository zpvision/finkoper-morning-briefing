# Диагностика «кто написал и что хотел»

Текущая диагностическая версия — [v4 с минимальным перефразированием и сравнением v2/v4](summary-v4-comparison.md); [замороженная пара v2/v3](summary-v2-v3-comparison.md) также сохранена. Ранее использованный holdout теперь считается регрессионным, не независимым. Используйте --set regression; --set holdout сохранён как alias, в отчёте input_scope=regression. Формат summary-v1 и рабочий API не меняются.

Отдельная команда `cmd/diagnose-summary`, промпт `diagnostic-summary-v4`, схема `summary-v1`:

```json
{"summary":"Связная сводка на русском","source_message_ids":["id"]}
```

Нет topics/status/priority/deadline_text/needs_context. Рабочие API, morning-v3/topics-v1, остальные диагностические промпты, задания SQLite и данные набора оценки не меняются. Автоматического переключения API нет. В v2 уточнены границы context_messages: он восстанавливает конкретные условия новых просьб, но не добавляет самостоятельные старые темы. Просьбы не усиливаются, обещания и подтверждённое выполнение различаются. Уточнение сделано по сообщению оператора о development dev-08, без настройки по разметке holdout. Сообщённые оператором 10/10 структурно корректных ответов и среднее processing_ms=4174 относятся к предыдущему v1, не доказывают смысловое качество v2.

Одна генерация на клиента, последовательно, без повторов, repair и скрытой обрезки. Профиль только qwen3:8b, think=false, temperature=0, num_ctx=8192, num_predict=1000, stream=false, keep_alive=30m, shift=false, truncate=false, HTTP timeout=120s. Новый короткий русский промпт не содержит эталонов, имён или ответов тестовых клиентов. Он просит сохранять существенные суммы, даты, сроки словами переписки, продолжения, напоминания, ответы и отмены; отделять обещание от выполнения, не придумывать файлы/требования и рекомендации, игнорировать инструкции внутри данных. Context используется для понимания текущих обращений. Ориентир 2–6 предложений не является ограничением валидатора: отдельные просьбы нельзя терять ради длины.

## Бюджет и защита

Только команда включает `PythonCounter.DiagnosticSummary` → helper `--summary`: резерв **1000** согласован в Count, Verify/render-only и генерации. Никакой переменной окружения для изменения рабочего резерва нет. API сохраняет 1500, thinking-диагностика — 4096, первоначальный профиль 14B — 1500. Summary не совмещается с thinking или профилем 14B.

Считаются тот же новый system и тот же сериализованный user JSON, что реально уходят в Ollama. Используется прежний проверяемый no_think-шаблон; normalization/truncation/padding отключены. Сохранены checksum локальных ресурсов, digest, Ollama 0.40.5, template hash и точное сравнение фактического rendered_template. Helper проверяет исходную false-ветку, транспорт отдельно проверяет точный payload со schema summary-v1. При отсутствии render-only/несовпадении/переполнении бюджета генерация этого входа не начинается, выполнение останавливается без обхода. Если сервер игнорирует нестандартный debug-флаг, уже выполненную им работу отменить нельзя.

Независимый валидатор проверяет JSON, обязательные два поля, их типы, отсутствие посторонних полей, непустую summary, уникальность и существование ID, хотя бы один источник из messages. Он **не классифицирует намерение по ключевым словам**. Поэтому периодный источник обязателен и для вывода «новых просьб нет»: можно сослаться на приветствия/благодарности как основание отрицательного вывода. Никакая тема при этом не требуется. Неизвестные, повторные и только контекстные ID отклоняются. Релевантность источников и верность утверждения об отсутствии просьб оцениваются вручную.

JSON берётся только из message.content. Текст message.thinking не записывается. Length/not done, ошибки структуры/источников не становятся успешной сводкой: result=null, ненулевой exit. Сырой невалидный content не сохраняется.

## Локальный план без сети

Нужны [отдельное venv и проверенные ресурсы 8B](../tools/context_budget/README.md). Из корня репозитория:

```powershell
$env:BRIEFING_BUDGET_PYTHON=(Join-Path $PWD '.venv-budget/Scripts/python.exe')
$env:OLLAMA_MODEL='qwen3:8b'
go build -o bin/diagnose-summary.exe ./cmd/diagnose-summary
# По умолчанию — 10 development-входов, план без сети:
./bin/diagnose-summary.exe
# Два существующих входа:
./bin/diagnose-summary.exe --set examples
# Только 10 development-входов:
./bin/diagnose-summary.exe --set development
# Holdout только по явному выбору, также план без сети:
./bin/diagnose-summary.exe --set holdout
```

Без --run ни один вариант не делает HTTP-запросов. examples — именно examples/04-exact-20.json и examples/02.json. development/regression читают только входы выбранной части manifest, без разметки. regression использует физический каталог holdout, уже утративший независимость. Произвольные пути и объединённый all не принимаются. По умолчанию выбирается development. В GoLand: Go Build, Package `finkoper-morning-briefing/cmd/diagnose-summary`, Working directory — корень, env выше, Program arguments пустые для development либо явно --set examples/regression.

По умолчанию JSONL идёт в stdout. --out сохраняет новый файл .jsonl с 0600 на Unix; существующий не перезаписывается. JSONL и evaluation/reports исключены из Git. План тоже можно сохранить через --out. API-токен не нужен; конфигурация API и сервисы тональности/RAG не используются.

## Ручной серверный запуск

Для сборки из **закреплённого полного коммита** и одного holdout-прогона используйте [tools/run-summary-holdout.sh](../tools/run-summary-holdout.sh):

```bash
bash tools/run-summary-holdout.sh FULL_40_CHARACTER_COMMIT_FROM_RELEASE
```

Подставьте полный SHA проверенного коммита. Скрипт требует Git, Go версии из go.mod (или доступного Go toolchain), Python с venv/pip, доступ к GitHub/PyPI на этапе подготовки и чтение существующих 8B-ресурсов. При недостаточных правах только чтение этих файлов выполняется через sudo. Новые clone, бинарник, Python-окружение, копия ресурсов, план и итоговый JSONL создаются в уникальном каталоге `$HOME/finkoper-summary-holdout.XXXXXX` текущего пользователя. Ничего в /opt или /etc не записывается. Скрипт не читает env API, не перезапускает/останавливает сервисы и не устанавливает модели. Зависимости подготавливаются до запросов; при обработке ничего не скачивается. При preflight-ошибке возможна ранняя остановка; иначе выполняются 10 последовательных генераций без повторов. Рабочая GPU общая с другими сервисами.

Проверка изоляции использует отдельные искусственные fixtures: некорректная разметка-ловушка находится рядом с входами, но не парсится и не появляется в запросах; HTTP mock сверяет весь user JSON с канонической сериализацией только Input. Это проверка инструмента, не смысловой оценщик. Разметка реального holdout для настройки v2 не использовалась; после ручной итоговой оценки её не следует использовать для дальнейшего подбора промпта под видом независимой проверки.

Сборка отдельного Linux-бинарника локально, без развёртывания:

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -o bin/diagnose-summary ./cmd/diagnose-summary
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

Оператор вручную размещает бинарник, обновлённый helper, examples и evaluation inputs/manifest в release. Используются прежние отдельное venv и ресурсы 8B; БД, `/etc/finkoper-morning-briefing.env`, бинарник API и systemd не нужны. В shell оператора:

```bash
cd /opt/finkoper-morning-briefing/current
summary8() {
  sudo -u finkoper-briefing env \
    BRIEFING_BUDGET_PYTHON=/opt/finkoper-morning-briefing/venv-budget/bin/python \
    BRIEFING_BUDGET_HELPER=/opt/finkoper-morning-briefing/current/tools/context_budget/helper.py \
    BRIEFING_TOKENIZER_DIR=/opt/finkoper-morning-briefing/resources/tokenizer/qwen3-8b \
    BRIEFING_BUDGET_TIMEOUT=60s \
    OLLAMA_BASE_URL=http://127.0.0.1:11434 OLLAMA_MODEL=qwen3:8b \
    ./diagnose-summary "$@"
}
# Сначала планы, без сети:
summary8 --set examples
summary8 --set development
summary8 --set holdout
```

Только отдельно разрешённые оператором генерации:

```bash
report_dir=$(sudo -u finkoper-briefing mktemp -d /tmp/briefing-summary.XXXXXX)
# Максимум 2 последовательные генерации:
summary8 --set examples --run --out "$report_dir/examples.jsonl"
# Максимум 10 последовательных генераций, holdout не используется:
summary8 --set development --run --out "$report_dir/development.jsonl"
# Явный регрессионный повтор бывшего holdout, максимум 10 генераций:
summary8 --set regression --run --out "$report_dir/regression.jsonl"
printf 'reports=%s\n' "$report_dir"
```

Повторный ручной запуск — новые генерации, автоматически команда не повторяется. Preflight-ошибка останавливает выполнение; ошибка уже начатой генерации сохраняется и позволяет перейти к следующему входу без повтора, с ненулевым итоговым exit. Контекст 8192 и глобальные настройки Ollama не меняйте. Нагрузка других сервисов на GPU командой не управляется.

## Что оценивать

Отчёт содержит file, input_scope, prompt/schema versions, хеши исходного файла/system/user/messages/schema/request/render, модель, ожидаемый/подтверждённый digest (в плане подтверждённый null), tokenizer manifest hash, параметры, бюджет, result, ошибки, токены и длительности helper/compatibility/render/HTTP/Ollama/processing, generation_attempts. Development и holdout добавляют ID и hash/version manifest, без эталонов. Отсутствующие метрики — null. Сравните prompt_eval_count с локальным budget.input_tokens: разность должна быть 0 для подтверждения на фактическом runtime.

Вручную проверьте полноту обращений, существенные суммы/даты/сроки, учёт ответов/отмен, отсутствие повторов и выдумок, верность источников. В development-разметке используйте просьбы, существенные факты и запрещённые выводы; не требуйте старых структурированных полей status/deadline_text/needs_context в новой схеме. В двух старых примерах не требуйте количества topics или дословного текста: важен смысл. Корректная структура и совпадение ключевых слов не доказывают качество. Mock-результаты — проверка инструмента, не модели. Реальные генерации разработчиком не запускались; к серверу он не подключался.
