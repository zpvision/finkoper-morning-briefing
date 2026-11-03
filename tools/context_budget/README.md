# Счётчик контекста: подготовка, подсчёт и подтверждение совместимости

Для [одиночной пробы thinking-long](../../docs/qwen35-thinking-long.md) helper поддерживает резерв 6144 при неизменном контексте 8192. Новый `verify_qwen35.py` проверяет существующие ресурсы только на чтение; он не отключает последующие runtime-проверки и не объявляет качество подтверждённым.

Для Qwen3.5 9B добавлен [независимый диагностический профиль](../../docs/qwen35-9b-profile.md): `prepare_qwen35.py`, отдельные pinned assets/manifest и `qwen35_helper.py`. Он не использует renderer/tokenizer 8B, не подключается автоматически к Ollama и не меняет рабочий счётчик. Подтверждение runtime-рендеринга и сверка числа токенов пробы отмечаются отдельно.

Его явный CLI-only флаг `--qwen35-mode baseline|greedy|thinking` согласует renderer, sampling и полный резерв (1000/4096) с [режимами diagnose-qwen35](../../docs/qwen35-mode-comparison.md). Manifest и ресурсы не меняются; режим рабочего API этим флагом не настраивается.

Для [новой summary-диагностики](../../docs/summary-diagnostics.md) есть явный флаг helper `--summary`: только 8B/think=false, резерв 1000 в подсчёте и render-only. По умолчанию рабочий резерв остаётся 1500; прежние профили не меняются.

Отдельный проверенный диагностический [профиль 14B](../../docs/qwen3-14b-profile.md) готовится инструментом `prepare_14b_profile.py` в новую директорию. Ресурсы 8B не перезаписываются. Helper принимает manifest 14B только с явным `--diagnostic-profile qwen3-14b-q4-k-m-v1`; профиль рабочего сервиса по умолчанию не меняется.

helper.py адаптирован из предоставленного context_budget.py. Сохранены рендеринг, контрольные суммы, проверка digest/шаблона, версия Ollama 0.40.5 и отключение нормализации. Рабочий режим — ровно system+user, think=false, резерв 1500. Явный флаг `--thinking` предназначен только для [отдельной диагностики think=true](../../docs/thinking-diagnostics.md), с резервом 4096 и обязательной проверкой format=schema. Сторонний HTTP-клиент не нужен. Диагностический оригинал в корне, архив и экспорт /api/show исключены из Git. Их исходные файлы и сервис тональности не изменяются.

## 1. Подготовка ресурсов — без сети и весов

Нужен Python 3.10+; prepare_resources.py использует только стандартную библиотеку. Из корня проекта:

```powershell
py tools/context_budget/prepare_resources.py --archive qwen3-tokenizer.tar.gz --show qwen3-show.json
# Только если выходного каталога ещё нет:
py tools/context_budget/prepare_resources.py --archive qwen3-tokenizer.tar.gz --show qwen3-show.json --output resources/tokenizer/qwen3-8b
```

На Linux заменить `py` на `python3`. Проверка не меняет входы. Копирование разрешено только в новый отдельный каталог; существующий не перезаписывается. Читаются три фиксированных обычных файла архива, без произвольной распаковки путей. Проверяются SHA-256 файлов, хеш шаблона, соответствие словаря/added_tokens ID и полный порядок merges против экспорта GGUF. Ошибки — безопасные коды без содержимого файлов.

На предоставленных ресурсах проверены 151643 BPE vocabulary entries, 151936 GGUF vocabulary entries, 151387 merges. Manifest: repository=Qwen/Qwen3-8B, revision=b968826d9c46dd6066d109eabc6255188de91218, model_digest=500a1f067a9f782620b40bee6f7b0c89e17ae61f686b92c24933e4ca4b2b8b41, ollama_template_sha256=ae370d884f108d16e7cc8fd5259ebc5773a0afa6e078b11f4ed7e39a27e0dfc4. Model digest — идентификатор manifest модели, не SHA весового blob FROM.

Подготовка возвращает runtime_compatibility_verified=false. Она подтверждает локальные ресурсы, но не фактический сервер.

## 2. Отдельное окружение helper

```powershell
py -m venv .venv-budget
.\.venv-budget\Scripts\python.exe -m pip install --no-deps -r tools/context_budget/requirements.txt
$env:BRIEFING_BUDGET_PYTHON=(Join-Path $PWD '.venv-budget/Scripts/python.exe')
$env:BRIEFING_BUDGET_HELPER=(Join-Path $PWD 'tools/context_budget/helper.py')
$env:BRIEFING_TOKENIZER_DIR=(Join-Path $PWD 'resources/tokenizer/qwen3-8b')
$env:BRIEFING_BUDGET_TIMEOUT='30s'
go run ./cmd/budget examples/01.json examples/02.json examples/04-exact-20.json
```

Установлен только tokenizers==0.22.1 из бинарного wheel; --no-deps исключает загрузочные клиенты Hugging Face, которые не нужны при чтении локального tokenizer.json. Установка выполняется отдельно от обработки заданий. Helper проверяет версию tokenizers и принадлежность Python виртуальному окружению. Он не использует Transformers, PyTorch, веса, CUDA, GPU или runtime-скачивание ресурсов.

`cmd/budget` без --verify работает полностью локально. Он печатает только имя файла, accepted, input_tokens, output_reserve=1500, context_tokens=8192, required_tokens, remaining_tokens, method и compatibility_verified. Этот режим не создаёт задания и не выполняет генерацию; compatibility_verified=false. Локально с morning-v3 получено 2391/1665/2370 токенов для трёх файлов соответственно; они проходят с резервом. Заведомо длинный вход отклоняется, без обрезки.

В результате задания `budget_helper_ms` измеряет worker-подсчёт, `compatibility_check_ms` — отдельную проверку совместимости перед генерацией, включая её Python subprocess и HTTP-запросы. Сумма не включает генерацию и входит в processing_ms; HTTP-приём и startup-probe измерениями задания не охватываются. При пропущенном этапе — null, при выполненном этапе с округлением до 0 ms — 0. Проверки совместимости остаются включёнными.

Нормализация tokenizer.normalizer=None, no_truncation(), no_padding(); текст генерации не изменяется. Считается весь шаблон с /no_think и assistant prefix через encode(add_special_tokens=False). Общая функция Go PromptInput формирует system и Go-сериализованный user JSON для счётчика, проверки рендеринга и генерации. Это исключает расхождения из-за повторной сериализации JSON другим языком.

## 3. Проверка совместимости — оператор запускает явно

Проверка выполняет GET /api/version, GET /api/tags, POST /api/show, затем один POST /api/chat с `_debug_render_only=true`, think=false, truncate=false, shift=false; повторяет проверку digest/шаблона после рендеринга. Сверяются версия 0.40.5, digest manifest, хеш шаблона и **точное равенство строк** в `_debug_info.rendered_template`. Поля нет → context_render_only_unavailable; строки отличаются → context_chat_render_mismatch. Успешная обычная генерация без поля не принимается. Автоматического retry, think=true и fallback к обычной генерации нет.

`_debug_render_only` — нестандартная возможность вашего сервера. Команда предназначена для сервера, где она реализована: сервер, игнорирующий неизвестное поле, может выполнить обычный запрос до того, как отсутствие поля будет обнаружено. Helper не повторяет такой запрос. Render-only не требует генерации, но Ollama может загрузить/перезагрузить runner. Сам helper веса не читает. Из локального прогона нельзя сделать вывод о GPU/загрузке сервера.

Приложение проверяет искусственную Unicode/whitespace-пробу перед запуском listener; worker перед каждой генерацией проверяет фактические system+user. Сбой блокирует запуск или завершает задание failed до генерации. После неудачной runtime-проверки новые задания получают 503, пока совместимость не будет подтверждена снова (обычно после устранения причины и перезапуска). Существующие идемпотентные задания возвращаются без проверки helper. Во время работы профиль, модель и конфигурация должны оставаться неизменными: атомарности между отдельными HTTP-запросами проверки и генерации Ollama не предоставляет.

## 4. Точные команды для вашей серверной установки

Эти команды подготовлены для оператора; агент их на сервере не выполнял. Они не затрагивают тональность. Предполагается, что исходники tools/, examples/, архив и qwen3-show.json доставлены оператором в `/opt/finkoper-morning-briefing/current`.

Сначала локально собрать три Linux-бинарника:

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -o bin/briefing ./cmd/briefing
go build -o bin/budget ./cmd/budget
go build -o bin/evaluate ./cmd/evaluate
Remove-Item Env:GOOS, Env:GOARCH
```

После ручной доставки бинарников briefing, budget и evaluate в current — подготовка отдельного venv и ресурсов на сервере:

```bash
cd /opt/finkoper-morning-briefing/current
sudo python3 -m venv /opt/finkoper-morning-briefing/venv-budget
sudo /opt/finkoper-morning-briefing/venv-budget/bin/python -m pip install --no-deps -r tools/context_budget/requirements.txt
python3 tools/context_budget/prepare_resources.py --archive qwen3-tokenizer.tar.gz --show qwen3-show.json
# Только для нового выходного каталога:
sudo python3 tools/context_budget/prepare_resources.py --archive qwen3-tokenizer.tar.gz --show qwen3-show.json --output /opt/finkoper-morning-briefing/resources/tokenizer/qwen3-8b
sudo chown -R root:finkoper-briefing /opt/finkoper-morning-briefing/resources/tokenizer
sudo find /opt/finkoper-morning-briefing/resources/tokenizer -type d -exec chmod 0750 {} +
sudo find /opt/finkoper-morning-briefing/resources/tokenizer -type f -exec chmod 0640 {} +
```

Убедиться, что пользователь finkoper-briefing может читать helper.py, проходить по родительским каталогам и запускать отдельный Python. Не использовать venv/каталог ресурсов тональности. В `/etc/finkoper-morning-briefing.env` сохранить существующий API-токен и задать:

```dotenv
BRIEFING_LISTEN_ADDR=127.0.0.1:18084
BRIEFING_DB_PATH=/var/lib/finkoper-morning-briefing/briefing.db
OLLAMA_BASE_URL=http://127.0.0.1:11434
OLLAMA_MODEL=qwen3:8b
OLLAMA_TIMEOUT=120s
BRIEFING_BUDGET_PYTHON=/opt/finkoper-morning-briefing/venv-budget/bin/python
BRIEFING_BUDGET_HELPER=/opt/finkoper-morning-briefing/current/tools/context_budget/helper.py
BRIEFING_TOKENIZER_DIR=/opt/finkoper-morning-briefing/resources/tokenizer/qwen3-8b
BRIEFING_BUDGET_TIMEOUT=60s
```

**До перезапуска сервиса** выполнить от его пользователя (токен API не нужен):

```bash
cd /opt/finkoper-morning-briefing/current
sudo -u finkoper-briefing env \
  BRIEFING_BUDGET_PYTHON=/opt/finkoper-morning-briefing/venv-budget/bin/python \
  BRIEFING_BUDGET_HELPER=/opt/finkoper-morning-briefing/current/tools/context_budget/helper.py \
  BRIEFING_TOKENIZER_DIR=/opt/finkoper-morning-briefing/resources/tokenizer/qwen3-8b \
  BRIEFING_BUDGET_TIMEOUT=60s \
  OLLAMA_BASE_URL=http://127.0.0.1:11434 OLLAMA_MODEL=qwen3:8b \
  ./budget --verify --probe examples/01.json examples/02.json examples/04-exact-20.json
```

Критерий успеха: exit 0, synthetic_probe_verified=true, у трёх файлов accepted=true и compatibility_verified=true. Это проверка версии/digest/рендеринга и подсчёта выбранным локальным токенизатором, не доказательство смыслового качества модели или математической эквивалентности всех вариантов токенизации. Для независимого подтверждения на реальных вызовах дополнительно сопоставить input_tokens с prompt_eval_count; расхождение требует расследования, а не ослабления бюджета. Обычную генерацию для такой сверки оператор запускает отдельно, осознанно, через cmd/evaluate или API.

При любой ошибке команду не заменять генерацией и не обходить проверку; сохранить безопасный код ошибки. systemd-пример использует `/opt/finkoper-morning-briefing/current/briefing`, env `/etc/finkoper-morning-briefing.env`, пользователя finkoper-briefing и БД в `/var/lib/finkoper-morning-briefing`. Unit не устанавливался, сервис автоматически не перезапускался, Nginx/firewall/Ollama settings не менялись.

## 5. Локальные тесты без реального AI-сервера

```powershell
.\.venv-budget\Scripts\python.exe -m unittest discover -s tools/context_budget -p 'test_*.py' -v
$env:BRIEFING_TEST_BUDGET_PYTHON=(Join-Path $PWD '.venv-budget/Scripts/python.exe')
go test ./... -count=1
Remove-Item Env:BRIEFING_TEST_BUDGET_PYTHON
go test ./...
go vet ./...
$env:CGO_ENABLED='0'
go build ./...
```

Без BRIEFING_TEST_BUDGET_PYTHON обычные Go-тесты не требуют Python/ресурсы, не обращаются к реальному серверу и явно используют stand-in счётчик. Дополнительный прогон использует настоящий subprocess/tokenizers с локальными ресурсами и HTTP mock на loopback. Проверяются оба примера и точный payload, переполнение с диагностикой 422, сохранение Unicode, контрольные суммы, ошибки version/digest/template/render-only, отсутствие генерации при сбое проверки, таймаут, ограничение stdout и отсутствие утечки stderr. Mock не доказывает качество модели или совместимость фактической Ollama.
