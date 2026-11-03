# Отдельная диагностика Qwen3.5 9B

`cmd/diagnose-qwen35` использует только профиль `qwen35-9b-summary-v2-v1`: `qwen3.5:9b`, digest `56671c2ab9385f9cfcb404638e32cd62d88e3501d44822208363c010179a3c90`, неизменённые `diagnostic-summary-v2` и `summary-v1`. Это связный пересказ, а не новый промпт с цитатами. API, morning-v3, SQLite, прежние диагностические профили и ресурсы 8B не переключаются.

Условия: system+user, только текст, без tools/изображений; `think=false`, `stream=false`, `keep_alive=30m`, контекст 8192, резерв/`num_predict` 1000, `shift=false`, `truncate=false`. Одна последовательная генерация на вход, без повторов и repair. Проверяются done/done_reason, полный JSON и источники; thinking отдельно от content, его текст не сохраняется. Структурная проверка не оценивает смысл.

## Источники и sampling

Все URL содержат закреплённые revision/commit. [Manifest](../internal/diagnostic/profiles/qwen35-9b-v1.json) содержит SHA256 и размер **каждого** исходного файла; его собственный SHA256 — `e9aeb0ccd5b2d1f2f952ba1486055c57e8635a93c693c3924b23904ddf9e1e51`. Go-бинарник встраивает manifest, Python проверяет его закреплённый хеш. Подготовка и каждый подсчёт проверяют файлы, а не доверяют одному имени каталога.

* Официальный [Qwen/Qwen3.5-9B](https://huggingface.co/Qwen/Qwen3.5-9B/tree/c202236235762e1c871ad0ccb60c8ee5ba337b9a): revision `c202236235762e1c871ad0ccb60c8ee5ba337b9a`, tokenizer.json/config, chat_template.jinja, config и README.
* [Ollama v0.40.5](https://github.com/ollama/ollama/tree/d919272e53593d1d55d40e1180eaa06dfbede3ac): commit `d919272e53593d1d55d40e1180eaa06dfbede3ac`, renderer, registry, parser, server routes/prompt/resolution, llama runner и привязка llama.cpp.
* [llama.cpp](https://github.com/ggml-org/llama.cpp/blob/620d2225cc436ab0b001e3200e590e3b286ef9ab/src/llama-vocab.cpp): commit `620d2225cc436ab0b001e3200e590e3b286ef9ab` (b11669 из Ollama), BPE pre-tokenizer qwen35 и учёт prompt_eval_count в server-context.

В [официальной карточке модели](https://huggingface.co/Qwen/Qwen3.5-9B/blob/c202236235762e1c871ad0ccb60c8ee5ba337b9a/README.md) для non-thinking/general приведены `temperature=0.7`, `top_p=0.8`, `top_k=20`, `min_p=0`, `presence_penalty=1.5`, `repetition_penalty=1.0`. Передаём все явно; последний параметр называется `repeat_penalty` в Ollama. Это **отличается** от прежнего 8B/temperature=0. Отчёт отмечает, что сравнение меняет модель, tokenizer, renderer и sampling; оно не изолирует эффект одной архитектуры. Thinking у модели включён по умолчанию, поэтому false передаётся явно во всех проверках и генерации.

Версия `/api/version` не доказывает git SHA собранного серверного бинарника. Публичный тег закрепляет изученную реализацию; фактический runtime проверяется отдельно, без предположений о совместимости семейства.

## Что подтверждено локальными экспортами

Подготовка сопоставила весь словарь, все merges и added tokens официального 9B tokenizer с verbose `/api/show`: 248320 записей GGUF, 247587 merges, 248044 BPE vocab + 26 added tokens HF; оставшиеся GGUF записи зарезервированы. Проверяется канонический SHA256 всех полей `tokenizer.*`, конфигурация специальных токенов и их ID. Архитектура/pre-tokenizer — qwen35, eos=248046, pad=248044, add_bos=false. Никакой BPE реализации в проекте нет: используется отдельный `tokenizers==0.22.1`.

Qwen35 pre-tokenizer отличается от 8B: в регулярном выражении учитывается `\p{M}`, цифры делятся по одной. HF NFC normalizer отключается **в памяти** для соответствия GGUF BPE; исходный текст в запросе не нормализуется и не изменяется. Проверяются SHA256 оригинальных tokenizer и исходников. Веса не загружаются, GPU не используется, во время обработки входа ничего не скачивается.

`TEMPLATE {{ .Prompt }}` — не полный chat template. При RENDERER qwen3.5 Ollama вызывает `Qwen35Renderer`, а не шаблон 8B и не автоматически HF Jinja. Для ограниченного system+user/think=false изученный renderer даёт:

```text
<|im_start|>system\nSYSTEM<|im_end|>\n
<|im_start|>user\nUSER_JSON<|im_end|>\n
<|im_start|>assistant\n<think>\n\n</think>\n\n
```

Здесь `\n` обозначает байт LF, строки отображены раздельно для чтения. Нет двойного переноса после system и `/no_think` из 8B. Считается тот же system и сериализованный user JSON, которые передаются генерации, включая специальные токены. Фиксированный system и JSON не имеют внешних пробелов; произвольный ввод с ними helper отклоняет, а не приближает Go TrimSpace другой функцией. Обрезка и padding токенизатора отключены. Превышение `input_tokens + 1000 > 8192` останавливает команду до HTTP.

## Runtime: два разных подтверждения

`--check-profile` проверяет версию, digest, metadata, renderer/parser и точное побайтовое совпадение `_debug_info.rendered_template`. В существующем транспорте дополнительно проверяется полный запрос с JSON Schema summary-v1 в `format`. Нужен именно фактический render-only; отсутствие строки — `qwen35_render_only_missing_rendered_template`, расхождение — `qwen35_runtime_renderer_mismatch` или `context_chat_render_mismatch`. Подмена обычной генерацией запрещена. Если сервер не поддерживает эту возможность для нового renderer, необходим механизм возврата **итогового system+user prompt после Qwen35Renderer до Completion**; команда его не обходит.

В изученном `_debug_info` нет runner token IDs/count. Поэтому успешный check подтверждает рендеринг, но **не** полную runtime-совместимость токенизатора: поле `runtime_token_count_verified` ещё отсутствует, локальный бюджет пока основан на закреплённых ресурсах и исходниках. Для проверки счётчика без генерации не хватает runtime-механизма, возвращающего token IDs/count именно финального rendered prompt при тех же настройках special tokens. Публичный `/api/tokenize` не предполагается, порты runner не ищутся, сервер не модифицируется.

После разрешённого оператором одиночного `--run` сравниваются локальный input_tokens и фактический `prompt_eval_count` (он уже включает cached prefix). Равенство подтверждает **число токенов данного запроса**, а не все возможные строки или совпадение каждого token ID. Отсутствие/расхождение даёт `qwen35_runtime_prompt_eval_count_missing` / `qwen35_runtime_token_count_mismatch`; результат не принимается, дальнейшие входы не запускаются. Проверка render-only сохраняется перед каждым вызовом. До реального прогона runtime-совместимость и смысловое качество не объявляются подтверждёнными.

## Локальная подготовка и план

Экспорты в корне исключены из Git, локальные копии сохраняются. Нужен verbose show с массивами tokens/merges/token_type. Подготовка создаёт **новый** каталог, ничего не перезаписывает:

```bash
python3 -m venv .venv-budget
.venv-budget/bin/python -m pip install -r tools/context_budget/requirements.txt
.venv-budget/bin/python tools/context_budget/prepare_qwen35.py \
  --manifest internal/diagnostic/profiles/qwen35-9b-v1.json \
  --show qwen35-9b-show.json --tags ollama-tags.json --version ollama-version.txt \
  --download --output resources/tokenizer/qwen35-9b
export BRIEFING_BUDGET_PYTHON="$PWD/.venv-budget/bin/python"
export OLLAMA_MODEL=qwen3.5:9b
go run ./cmd/diagnose-qwen35 --profile qwen35-9b-summary-v2-v1 --case six
```

Только подготовка с `--download` обращается к закреплённым публичным ресурсам (без весов). Вместо него доступен `--source-dir CACHE` с файлами, названными ключами `sources` manifest. Сам план сети не использует. В PowerShell задайте абсолютный путь к `.venv-budget/Scripts/python.exe` через `$env:BRIEFING_BUDGET_PYTHON`; параметры команды те же. `--profile-resources` указывает отдельный каталог, настройки 8B игнорируются. Конфликтующий OLLAMA_MODEL явно отклоняется.

Шесть **существующих регрессионных** входов: sr-01, sr-04, sr-06, sr-07, hol-05, hol-09. Имена hol не означают независимый holdout после предыдущего использования. По умолчанию выбран sr-01; `--case six` выбирает все шесть в указанном порядке. Читаются только JSON входов, разметка не читается и не передаётся модели. Эталоны не меняются.

Локальный план для закреплённых входов и v2: соответственно 704, 835, 834, 775, 867, 757 входных токенов; у каждого дополнительно резерв 1000. Эти значения ещё не сверены с реальной Ollama.

План и отчёты содержат версии/хеши system, user, schema, запроса, manifest, исходного файла, digest, параметры, локальный бюджет, результат/ошибки и метрики (helper, совместимость, schema render-only, HTTP/Ollama, токены). Признак model_digest появляется после проверки runtime. Файлы `*.jsonl` исключены из Git, `--out` не перезаписывает существующий файл. Отчёты могут содержать сводки переписки; в этих входах данные синтетические.

## Ручной серверный запуск

Из **отдельного** checkout закреплённого коммита: `bash scripts/qwen35-profile-probe.sh`. Скрипт собирает отдельный бинарник, создаёт venv/ресурсы/JSONL в новом каталоге HOME, получает только metadata, проверяет профиль без генерации, затем выполняет один sr-01 с `--run`. При ошибке проверки проба не запускается; trap печатает каталог отчётов и код завершения. Ollama не обновляется, модель не скачивается, глобальные настройки и сервисы не меняются. Нужны уже установленная модель, Go, Python/venv, curl, доступ к pinned public assets и локальной Ollama.

После успешной пробы и ручной оценки можно отдельно запустить тот же бинарник с `--case six --run --out NEW.jsonl` и теми же `--profile`/`--profile-resources`; это максимум шесть последовательных генераций. Не делайте этот запуск автоматическим продолжением пробы. `ollama ps` и `nvidia-smi` во время теста — наблюдения о размещении, не доказательство качества. Переключение 8B/9B на общей GPU с тональностью и RAG может перезагружать модели и искажать замеры; даже render-only может запускать загрузку runner. Сервисы не останавливать автоматически.

## Проверки

```bash
go test ./...
go vet ./...
go build ./...
# Дополнительно, с подготовленными отдельными ресурсами:
BRIEFING_TEST_BUDGET_PYTHON="$PWD/.venv-budget/bin/python" go test ./cmd/diagnose-qwen35 -count=1
.venv-budget/bin/python -m unittest discover -s tools/context_budget -p test_qwen35_profile.py
bash -n scripts/qwen35-profile-probe.sh
```

HTTP mock проверяет реальную цепочку Go → subprocess helper → HTTP metadata/render-only → генерация-заглушка, отсутствие сетевых запросов в плане, один вызов, несовместимый digest/render/count, лимит и отсутствие thinking в отчёте. Это проверка инструмента, **не качества модели**. Реальная Ollama и сервер при разработке не вызываются.

При подготовке прошли обычные `go test ./...`, vet, сборки Windows/Linux, отдельный тест с настоящим CPU-helper и HTTP mock, Python-проверки ресурсов и bash syntax check. Дополнительный массовый параллельный opt-in прогон с CPU-helper обнаружил нестабильность старых тестов: очистка Windows TempDir и короткий HTTP timeout двухэтапной диагностики под нагрузкой. Их код и рабочий API в этой задаче не менялись; это ограничение расширенного параллельного прогона, а не доказательство ошибки или качества реальной модели.
