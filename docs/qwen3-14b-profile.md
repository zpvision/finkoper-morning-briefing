# Диагностический профиль qwen3:14b-q4_K_M

В `evaluate-batch` профиль `qwen3-14b-q4-k-m-v1` выбирается явно через --profile; его область остаётся development, think=false, morning-v3, topics-v1, другие режимы/holdout отклоняются. [Отдельная команда diagnose-files](standalone-14b-files.md) использует тот же pin для произвольных входных файлов, не присваивая им split. Это не переключение рабочего API. Контекст 8192, резерв ответа 1500, temperature=0, timeout 120s, stream=false, keep_alive=30m, shift=false, truncate=false. Нет повторов, repair и обрезки.

## Что подтверждено локально

Сопоставлены полные экспорты qwen3-show.json и qwen3-14b-show.json, не только семейство модели:

| Проверка | Результат |
|---|---|
| Все поля tokenizer.* | Совпадают, включая значения и порядок массивов |
| GGUF словарь и token_type | 151936 одинаковых записей с теми же ID и типами |
| HF BPE vocab | Все 151643 записи совпадают с GGUF по ID |
| Merges | Все 151387 правил совпадают в том же порядке |
| Added tokens | Все 26 совпадают по ID/тексту; special-флаги согласованы с GGUF control type и tokenizer_config |
| EOS / padding / BOS | EOS=151645, padding/BOS=151643; add_bos=false; HF eos/pad согласованы, автоматический BOS отключён |
| Ollama template | Побайтно одинаковый, SHA-256 ae370d884f108d16e7cc8fd5259ebc5773a0afa6e078b11f4ed7e39a27e0dfc4 |
| Defaults в экспортах parameters | Одинаковые: repeat_penalty=1, temperature=0.6, top_k=20, top_p=0.95, stop im_start/im_end; запрос переопределяет temperature=0 |
| Digest 14B из tags | bdbd181c33f2ed1b31c972991882db3cf4d192569092138a7d29e973cd9debe8 |

Отдельный [закреплённый manifest](../internal/evaluation/profiles/qwen3-14b-q4-k-m-v1.json) имеет SHA-256 `dc5b73c3845a1fdf043f3055a98b3b0399831dc27592d8f995db034f929489be`. Он встроен в диагностический бинарник. Helper принимает его только с явным диагностическим флагом и проверяет тот же pin, checksum файлов, Ollama 0.40.5, runtime digest и template hash. Подмена manifest, другого имени модели или ресурсов 8B приводит к отказу.

Ресурсы скопированы в **отдельный** `resources/tokenizer/qwen3-14b`. repository/revision в manifest честно указывают источник повторно использованных HF-файлов 8B; это не утверждение о скачивании HF 14B. Совместимость подтверждена полным сравнением GGUF метаданных 14B с этими файлами. Normalization отключена, токенизатор/шаблон/резерв рабочего 8B не менялись. Промпты не менялись.

Экспорты qwen3-14b-show.json и ollama-tags.json исключены из Git, как и исходный qwen3-show.json, веса, ресурсы и отчёты. В Git сохраняются только инструмент проверки и небольшой manifest с контрольными суммами. Локальная проверка не подтверждает текущий сервер: перед каждым запросом сохранены проверки фактического render-only, digest и версии.

## Повторная подготовка без сети

На компьютере с исходными экспортами и подготовленными ресурсами 8B:

```powershell
py tools/context_budget/prepare_14b_profile.py --source resources/tokenizer/qwen3-8b --source-show qwen3-show.json --target-show qwen3-14b-show.json --tags ollama-tags.json --expected-manifest internal/evaluation/profiles/qwen3-14b-q4-k-m-v1.json
```

Для создания новой отдельной директории добавьте `--output resources/tokenizer/qwen3-14b` (директория должна отсутствовать; существующая не перезаписывается). Источник читается без записи, веса и сеть не используются. `--manifest-output` нужен только разработчику для первоначального создания нового pin, не для обхода проверки установленной модели.

Соберите `go build -o bin/evaluate-batch.exe ./cmd/evaluate-batch`. Offline plan, ноль HTTP-запросов:

```powershell
$env:BRIEFING_BUDGET_PYTHON=(Join-Path $PWD '.venv-budget/Scripts/python.exe')
$env:OLLAMA_MODEL='qwen3:14b-q4_K_M'
./bin/evaluate-batch.exe --profile qwen3-14b-q4-k-m-v1 --split development --mode think-false --probe
```

Противоречащий OLLAMA_MODEL отклоняется. В отдельной диагностической конфигурации допускается также отсутствие OLLAMA_MODEL: имя тогда задаёт профиль. `BRIEFING_TOKENIZER_DIR` рабочего 8B для 14B не используется; путь задаётся отдельным `--profile-resources`, по умолчанию resources/tokenizer/qwen3-14b. Не редактируйте конфигурацию API ради эксперимента.

## Ручные команды на сервере

Эти команды подготовлены для оператора, разработчиком на сервере не выполнялись. Модель по tags уже существует; никаких pull/install/serve и глобальных настроек команда не выполняет. Отдельный бинарник для Linux соберите локально с GOOS=linux GOARCH=amd64 CGO_ENABLED=0, затем разместите вручную вместе с обновлённым helper и dataset. Подготовленный **отдельный** каталог qwen3-14b должен находиться в `/opt/finkoper-morning-briefing/resources/tokenizer/qwen3-14b`; venv прежний отдельный счётчика. Пользователь finkoper-briefing должен иметь права чтения и исполнения. API `/opt/finkoper-morning-briefing/current/briefing`, его env и БД не используются, перезапуск не требуется.

В shell оператора задайте функцию (env действует только на дочернюю диагностическую команду):

```bash
cd /opt/finkoper-morning-briefing/current
brief14() {
  sudo -u finkoper-briefing env \
    BRIEFING_BUDGET_PYTHON=/opt/finkoper-morning-briefing/venv-budget/bin/python \
    BRIEFING_BUDGET_HELPER=/opt/finkoper-morning-briefing/current/tools/context_budget/helper.py \
    BRIEFING_BUDGET_TIMEOUT=60s \
    OLLAMA_BASE_URL=http://127.0.0.1:11434 OLLAMA_MODEL=qwen3:14b-q4_K_M \
    ./evaluate-batch --profile qwen3-14b-q4-k-m-v1 \
      --profile-resources /opt/finkoper-morning-briefing/resources/tokenizer/qwen3-14b \
      --split development --mode think-false "$@"
}
```

1. Проверка профиля **без генерации** на первом development-примере:

```bash
brief14 --probe --check-profile
```

Это явное разрешение только на metadata/render-only HTTP. В отчёте mode=check, selected_examples=1, generation_attempts=0, budget.compatibility_verified=true и подтверждённый model_digest. Сравниваются точный system+user и format=topics-v1. Пустой rendered_template, несовпадение, другая версия/digest или превышение бюджета — отказ. По умолчанию без --check-profile и без --run сеть не используется. Одновременно --check-profile и --run запрещены. Render-only нестандартный; если сервер игнорирует его и генерирует, отсутствие поля приведёт к остановке без fallback, но уже выполненную сервером работу отменить нельзя.

2. Только после успешной проверки — **один** пробный вызов (`dev-01`), без повторов:

```bash
probe_dir=$(sudo -u finkoper-briefing mktemp -d /tmp/briefing-14b-probe.XXXXXX)
brief14 --probe --run --out "$probe_dir/probe.jsonl"
printf 'probe_report=%s\n' "$probe_dir/probe.jsonl"
```

Проверьте generation_attempts=1, отсутствие error/structural_validation_error, done=true, done_reason=stop, result, budgets и `prompt_eval_count_minus_local=0`. Если счётчик не совпал, не переходите к полному batch: исследуйте профиль, не ослабляя бюджет. Семантику dev-01 проверьте вручную по отдельной разметке. Повторный запуск --probe — ещё одна генерация, автоматически он не выполняется.

3. Только после просмотра probe — полный development batch, максимум 10 последовательных вызовов:

```bash
batch_dir=$(sudo -u finkoper-briefing mktemp -d /tmp/briefing-14b-development.XXXXXX)
brief14 --run --out "$batch_dir/development.jsonl"
printf 'batch_report=%s\n' "$batch_dir/development.jsonl"
```

Probe не является checkpoint: полный batch снова включает dev-01 (итого оператор разрешает 1+10 вызовов). Отчёты не смешивать; ни одного автоматического повтора нет. Все checks сохраняются на каждом примере. model_digest/diagnostic_profile, хеши prompt/schema/user/resources, токены и длительности сохраняются как прежде, thinking не записывается.

## Проверка размещения во время пробного вызова

Начните наблюдение **до** ручного --probe --run в другом терминале:

```bash
OLLAMA_HOST=127.0.0.1:11434 ollama ps
watch -n 1 'OLLAMA_HOST=127.0.0.1:11434 ollama ps'
```

По [официальной документации Ollama](https://docs.ollama.com/faq#how-can-i-tell-if-my-model-was-loaded-onto-the-gpu), PROCESSOR показывает размещение: 100% GPU, 100% CPU или смешанное CPU/GPU. Это не процент текущей загрузки GPU. Render-only может не загрузить веса, поэтому отсутствие модели в ps до генерации не доказывает проблему.

Параллельно, команды только чтения, остановка Ctrl+C:

```bash
nvidia-smi --query-gpu=timestamp,index,name,memory.total,memory.used,memory.free,utilization.gpu --format=csv -l 1
nvidia-smi --query-compute-apps=pid,process_name,used_gpu_memory --format=csv -l 1
```

Синтаксис мониторинга: [NVIDIA SMI](https://docs.nvidia.com/deploy/nvidia-smi/index.html). Сохраните наблюдения о размещении, VRAM, других процессах и времени вместе с ручной оценкой. В RTX 3060 12 GB вместе с RAG/тональностью полное размещение 14B на GPU не гарантируется; размер файла весов из tags сам по себе этого не доказывает. Ничего не выгружайте, не меняйте глобальные настройки Ollama, контекст или чужие сервисы ради этого теста. Эксперимент ограничивает только собственную последовательность запросов, а не общую GPU.

Локальные mock-тесты подтверждают защитные проверки и транспорт, не качество/скорость 14B и не её фактическое размещение. Реальные генерации и серверные проверки остаются ручными.
