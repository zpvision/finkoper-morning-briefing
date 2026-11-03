# Одиночная проба thinking-long

После реального sr-06/thinking с length, 4096 output tokens и пустым content добавлен явный `--mode thinking-long --case sr-06`. Весь набор не повторяется. Режим не включён в all и не выбран по умолчанию; другой --case (включая six и отсутствие явного sr-06) отклоняется до подготовки ресурсов и HTTP. baseline/greedy/thinking, diagnostic-summary-v2, summary-v1, manifest и ресурсы сохранены.

Think=true, sampling **полностью как thinking**: temperature=1, top_p=0.95, top_k=20, min_p=0, presence_penalty=1.5, repeat_penalty=1. Меняются только полный резерв/num_predict до 6144 и HTTP timeout до 600 секунд; контекст остаётся 8192. Счётчик, helper preflight, format-aware render-only, генерация и JSONL-отчёт используют одинаковые параметры. Требуется input_tokens + 6144 <= 8192, то есть вход не более 2048 токенов, без обрезки или увеличения контекста. Отдельный timeout subprocess helper прежний (30 с по умолчанию, 60 с в ручном скрипте).

Локальный план sr-06 с закреплёнными ресурсами: 832 входных токена + 6144 резерв = 6976, остаток 1216. Это локальный подсчёт; runtime и смысловое качество проверяются оператором при реальном запуске.

Thinking renderer прежний, без пустого закрытого think-блока. Проверяются файлы/контрольные суммы, версия Ollama, digest, точный runtime renderer, после генерации — prompt_eval_count. Совпадение счётчика **не делает** length или отсутствие финального JSON успехом. Валидируется только message.content: структура summary-v1, допустимые уникальные источники. Текст message.thinking не сохраняется, не печатается и не логируется; остаются только признак наличия и размер. Один вызов, без repair, повтора и fallback. Результат нужен для ручной оценки; mock подтверждает инструмент, не качество.

## Проверка существующих ресурсов

Новый `tools/context_budget/verify_qwen35.py` работает локально, только на чтение, без сети, загрузки весов и tokenizers. Он закрепляет manifest и проверяет SHA256/размер **каждого** исходного файла. Дополнительно можно передать все три экспорта для полной сверки с verbose show, tags и version:

```bash
python3 tools/context_budget/verify_qwen35.py \
  --resources /home/user85037/qwen35-comparison-reports.quKuMI/resources
# С дополнительной проверкой свежих экспортов:
python3 tools/context_budget/verify_qwen35.py --resources DIR \
  --show show.json --tags tags.json --version version.txt
```

Успешная проверка файлов не объявляет runtime-совместимость или качество подтверждёнными. Любая ошибка контрольной суммы — отказ, без перезаписи, ремонта или скачивания замены. Отсутствующий/нечитаемый файл также даёт ошибку. Сам helper сохраняет повторные проверки при обработке задания.

## Ручной запуск

В отдельном checkout закреплённого коммита выполните `bash scripts/qwen35-thinking-long.sh`. Скрипт создаёт каталог отчётов и venv в HOME, собирает отдельный бинарник, получает metadata без генерации и пытается использовать каталог `/home/user85037/qwen35-comparison-reports.quKuMI/resources`. Если каталог существует, обязательно запускается новый verifier с актуальными экспортами. Если отсутствует — в новом HOME-каталоге подготавливаются pinned tokenizer/source (без весов), затем проверяются тем же verifier. Повреждённый существующий каталог не заменяется автоматически.

Далее: один локальный план → check-profile без генерации → один --run только sr-06. Любая ошибка проверки предотвращает генерацию. EXIT trap печатает путь result.jsonl и код завершения даже при ошибке; повторного запуска нет. Файл с неуспешной генерацией сохраняется с ошибками и result=null. Предыдущие отчёты, ресурсы и работающие сервисы не меняются; Ollama не обновляется. На общей GPU возможны помехи от других сервисов, время только записывается.

Из корня проекта отдельные команды (нужны BRIEFING_BUDGET_PYTHON и подготовленные ресурсы):

```bash
go run ./cmd/diagnose-qwen35 --profile qwen35-9b-summary-v2-v1 --mode thinking-long --case sr-06
go run ./cmd/diagnose-qwen35 --profile qwen35-9b-summary-v2-v1 --mode thinking-long --case sr-06 --check-profile
# Только оператор вручную разрешает единственную генерацию:
go run ./cmd/diagnose-qwen35 --profile qwen35-9b-summary-v2-v1 --mode thinking-long --case sr-06 --run --out sr-06-long.jsonl
```
