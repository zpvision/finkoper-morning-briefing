# Qwen3.5 27B на RTX 4090: отдельная диагностика

Явный профиль `qwen35-27b-summary-v2-v1` в существующем diagnose-qwen35. Модель qwen3.5:27b-q4_K_M, digest 64ae5b1d651fb2709d5928ccb8dcfb4bb2cc9c2c8001ac34497e36a6cf905b59, Ollama 0.40.15, renderer/parser qwen3.5. Рабочие API, 8B и все режимы 9B не переключаются. Для 27B разрешён только baseline: think=false, stream=false, keep_alive=30m, temperature=0.7, top_p=0.8, top_k=20, min_p=0, presence_penalty=1.5, repeat_penalty=1, num_ctx=8192, num_predict/резерв=1000, HTTP timeout=120s, shift=false, truncate=false.

Неизменённые diagnostic-summary-v2 и summary-v1 предназначены для связного русского пересказа. Разметка не читается. План six включает sr-01, sr-04, sr-06, sr-07, hol-05, hol-09. Это регрессии, не независимый holdout. Генерация six запрещена: сначала только sr-06, затем отдельный remaining (sr-01, sr-04, sr-07, hol-05, hol-09) после успешной пробы. --probe-report требует JSONL успешного sr-06 того же профиля, manifest/digest, входа, промпта/schema, baseline-параметров, runtime token count и валидного результата. Это технический допуск; оператор отдельно оценивает смысл перед запуском остальных пяти.

Экспорты qwen27-show.json, ollama-tags.json (имя вложения ollama-tags(1).json) и ollama-version.json исключены из Git. Локально проверено полное равенство значений всех tokenizer.* экспортов 27B и 9B. Подготовка 27B также проверяет канонический хеш tokenizer metadata, полный vocab, merges, специальные токены и конфигурацию по закреплённым ресурсам 9B. Новый manifest — internal/diagnostic/profiles/qwen35-27b-v1.json, SHA256 50c7525a1398acb8971798701e6df3a4204f291f1c4d0fde8335a653c30787a6. Старый manifest не изменён. Источники токенизатора те же, со всеми прежними checksum, нормализация выключена. Никаких весов, GPU или загрузок при обработке входа.

Подготовка нового каталога:

```bash
python3 tools/context_budget/prepare_qwen35_27.py \
  --manifest internal/diagnostic/profiles/qwen35-27b-v1.json \
  --show qwen27-show.json --tags ollama-tags.json --version ollama-version.json \
  --source-dir resources/tokenizer/qwen35-9b --output resources/tokenizer/qwen35-27b
```

Вместо --source-dir можно явно --download: скачиваются только файлы по прежним закреплённым URL, без весов. Файлы 9B не перезаписываются. Helper 9B по умолчанию не принимает manifest 27B; отдельная явная точка входа 27B использует общий транспорт и CPU tokenizers. Версия 0.40.15 разрешена только фабрикой 27B; рабочая проверка 0.40.5 сохранена.

TEMPLATE {{ .Prompt }} не считается полным chat template. Сохранённый исходник renderer 0.40.5 — только кандидат/референс для 0.40.15; поле ollama_commit в manifest обозначает именно этот референс, а не подтверждённый commit нового бинарника. Реальный runtime пока не вызывался. Перед генерацией требуются metadata/digest/version и побайтовое равенство render-only system+user, включая format=schema. Нет rendered_template → qwen35_render_only_missing_rendered_template/context_render_only_unavailable; несовпадение → qwen35_runtime_renderer_mismatch/context_chat_render_mismatch. Нужен возврат итогового prompt после renderer до Completion. Обхода обычной генерацией нет.

Локальный бюджет учитывает тот же system/user и специальные токены: input_tokens+1000<=8192, иначе отказ до генерации. После генерации сравнивается prompt_eval_count: расхождение qwen35_runtime_token_count_mismatch немедленно останавливает прогон; отсутствие usage у завершённого ответа тоже ошибка. Render-only не подтверждает число token IDs; совпадение prompt_eval_count относится к конкретному запросу. Done/length, JSON и источники проверяются независимо. Текст thinking в отчёт не попадает. Один вызов на вход, последовательно, без повторов/repair/fallback. Ошибка отдельной генерации сохраняется; несовместимость останавливает следующие вызовы. JSONL содержит ответы/ошибки, case_id, профиль/digest, версии/хеши, параметры, бюджет, токены и длительности.

## Ручной запуск под root

Из закреплённого checkout внутри **существующего** tmux qwen27:

```bash
bash scripts/qwen27.sh probe "$HOME/qwen27-probe-$(date -u +%Y%m%dT%H%M%SZ)"
# После ручного просмотра успешной пробы, отдельная команда с тем же каталогом:
bash scripts/qwen27.sh remaining /root/ТОЧНЫЙ_КАТАЛОГ_ПРОБЫ
```

Probe собирает бинарник, отдельный venv/ресурсы, план без сети, check-profile без генерации и одну sr-06. Remaining не повторяет sr-06, использует тот же checkout/бинарник, снова проверяет ресурсы и runtime на каждом входе. Существующие JSONL не перезаписываются. Скрипт не скачивает модель, не обновляет Ollama и не управляет сервисами; работает с 127.0.0.1:11434. Длительный remaining запускайте вручную внутри tmux qwen27. Загрузка модели не доказывает размещение в GPU или качество; при необходимости наблюдайте ollama ps и nvidia-smi отдельно. Mock подтверждает инструмент, не смысловую точность.
