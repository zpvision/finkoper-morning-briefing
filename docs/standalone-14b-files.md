# Отдельные входные файлы на профиле 14B

`cmd/diagnose-files` использует существующие Runner/Compare варианта A и закреплённый профиль `qwen3-14b-q4-k-m-v1`. Входы — произвольные файлы формата API, переданные аргументами. Они не объявляются development или holdout: dataset manifest и разметка не читаются. `evaluate-batch`, рабочие API, morning-v3, topics-v1 и существующий набор не меняются.

Профиль обязателен и должен быть именно 14B. Модель `qwen3:14b-q4_K_M`, think=false, temperature=0, num_ctx=8192, num_predict/резерв=1500, stream=false, keep_alive=30m, timeout=120s, shift=false, truncate=false. Параметров переключения модели, thinking или промпта в команде нет.

Без --run выполняются только проверка файлов и локальный CPU helper: **сети и генераций нет**. С --run — максимум один вызов на файл, строго в порядке аргументов, без повторов, repair и обрезки. Повторная передача одного файла (включая альтернативный путь) отклоняется. Все входные структуры проверяются до начала сети; размер каждого файла ограничен 1 MiB, даты/ID/период и ограничения текстов проверяются как в API.

Все проверки профиля сохранены: точный manifest pin, checksum tokenizer-файлов, digest модели, Ollama 0.40.5, хеш шаблона, точное совпадение фактического rendered_template и system/user с format=topics-v1, отдельный бюджет с резервом 1500. При preflight-ошибке дальнейшие файлы не запускаются. После ошибки уже начатой генерации следующий файл обрабатывается без повтора предыдущего; итоговый exit code остаётся ненулевым. Length/not done/ошибки структуры и источников не дают успешный result. Нет fallback при отсутствии render-only. Нестандартный render-only, проигнорированный сервером, приводит к отказу по отсутствующему полю, но уже выполненную им работу отменить нельзя.

## Локальная сборка и план

После [подготовки отдельного профиля и ресурсов](qwen3-14b-profile.md), из корня репозитория:

```powershell
$env:BRIEFING_BUDGET_PYTHON=(Join-Path $PWD '.venv-budget/Scripts/python.exe')
$env:OLLAMA_MODEL='qwen3:14b-q4_K_M'
go build -o bin/diagnose-files.exe ./cmd/diagnose-files
./bin/diagnose-files.exe --profile qwen3-14b-q4-k-m-v1 examples/04-exact-20.json examples/02.json
```

Это план без сети. Исходные входы остаются вне синтетического набора. Для текущего morning-v3 локальный счётчик даёт 2370 и 1665 входных токенов соответственно, с резервом 3870 и 3165; фактический runtime проверяется только при --run. Противоречащий OLLAMA_MODEL (например, из окружения API 8B) отклоняется; также допустимо не задавать эту переменную в отдельном диагностическом окружении. BRIEFING_TOKENIZER_DIR рабочего 8B не используется: путь 14B задаёт --profile-resources, по умолчанию resources/tokenizer/qwen3-14b.

В GoLand: Go Build, Package `finkoper-morning-briefing/cmd/diagnose-files`, Working directory — корень, переменные выше; Program arguments:

```text
--profile qwen3-14b-q4-k-m-v1 examples/04-exact-20.json examples/02.json
```

## Только ручной запуск оператором

Сборка отдельного Linux-бинарника на локальном Windows, без загрузки/развёртывания:

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -o bin/diagnose-files ./cmd/diagnose-files
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

Оператор вручную размещает бинарник, helper и два входа в release. Прежние отдельные 14B-ресурсы остаются в `/opt/finkoper-morning-briefing/resources/tokenizer/qwen3-14b`. Никаких изменений API-конфига, БД, systemd или Ollama. В shell оператора:

```bash
cd /opt/finkoper-morning-briefing/current
files14() {
  sudo -u finkoper-briefing env \
    BRIEFING_BUDGET_PYTHON=/opt/finkoper-morning-briefing/venv-budget/bin/python \
    BRIEFING_BUDGET_HELPER=/opt/finkoper-morning-briefing/current/tools/context_budget/helper.py \
    BRIEFING_BUDGET_TIMEOUT=60s \
    OLLAMA_BASE_URL=http://127.0.0.1:11434 OLLAMA_MODEL=qwen3:14b-q4_K_M \
    ./diagnose-files --profile qwen3-14b-q4-k-m-v1 \
      --profile-resources /opt/finkoper-morning-briefing/resources/tokenizer/qwen3-14b "$@"
}
# План без сети:
files14 examples/04-exact-20.json examples/02.json
```

Реальные генерации разрешает только отдельная ручная команда с --run:

```bash
report_dir=$(sudo -u finkoper-briefing mktemp -d /tmp/briefing-14b-files.XXXXXX)
files14 --run --out "$report_dir/two-files.jsonl" examples/04-exact-20.json examples/02.json
printf 'report=%s\n' "$report_dir/two-files.jsonl"
```

Все флаги указывайте **до** первого файла. По умолчанию отчёт идёт в stdout, --out сохраняет в новый .jsonl без перезаписи существующего файла, с правами 0600 на Unix. JSONL исключены из Git. Можно сначала передать только один вход для одного пробного вызова. Повтор всей команды — новые генерации; автоматически команда не повторяется. [Наблюдение через ollama ps и nvidia-smi](qwen3-14b-profile.md#проверка-размещения-во-время-пробного-вызова) выполняется оператором отдельно.

## Отчёт и пределы проверки

Одна JSONL-строка на обработанный файл: `file` — исходное имя/путь ровно из аргументов; `input_scope=standalone_files`, без split/dataset-полей. Сохраняются hash исходного файла, сериализованного user, промпта, schema, запроса и рендера; версии prompt/schema, модель, профиль, ожидаемый digest и подтверждённый digest (null в плане), tokenizer manifest hash, параметры, бюджет, result, ошибки, число генераций и длительности helper/compatibility/render/HTTP/Ollama/processing. Отсутствующие метрики остаются null.

Сравните prompt_eval_count (`metrics.input_tokens`) и локальный budget.input_tokens: `prompt_eval_count_minus_local` должен быть 0 для подтверждения счётчика на фактическом сервере. Текст message.thinking не сохраняется; JSON проверяется только из message.content. Невалидный сырой content скрыт, при ошибке result=null. Отчёт может содержать итоговые сведения переписки, храните его отдельно от публичных логов.

Mock-тесты подтверждают транспорт, параметры, последовательность и защитные проверки, не качество модели. Смысловую точность двух примеров оператор оценивает вручную. Разработчик реальные генерации не выполнял и к серверу не подключался.
