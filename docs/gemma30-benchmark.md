# Gemma: 30 переписок и полное время партии

Существующий `tools/compare_alternatives.py` поддерживает отдельный `--suite gemma30`. Go bridge читает ровно `long-01.json`…`long-30.json`; `review.md` не открывается. Вход нормализуется и сериализуется прежним `briefing.PromptInput`, промпт — побайтно прежний `diagnostic-summary-v2`, схема — `summary-v1`. Другие режимы и рабочий API не меняются.

Единственный профиль — закреплённая `gemma4:26b`: think=false, temperature=0.7, top_p=0.8, top_k=20, min_p=0, presence_penalty=1.5, repeat_penalty=1, num_ctx=8192, num_predict/резерв=1000, timeout=600s. Прежние checksum/digest/version/tokenizer/renderer/GPU/runtime-count/JSON/source-ID проверки остаются. Несовместимость прекращает прогон, ошибочный ответ записывается без повтора. Готовые JSONL запрещено перезаписывать повторным запуском.

Из корня проекта, с уже работающим туннелем:

```powershell
go build -o bin/summary-contract.exe ./cmd/summary-contract
if ($LASTEXITCODE -ne 0) { throw 'build failed' }
$runArgs = @('--bridge','bin/summary-contract.exe','--input-dir','finkoper-gemma-30/inputs','--suite','gemma30','--base-url','http://127.0.0.1:11435','--out-dir','evaluation/reports/gemma30-20261106')
& .venv-budget/Scripts/python.exe tools/compare_alternatives.py @runArgs
if ($LASTEXITCODE -ne 0) { throw 'plan failed' }
# Один явный реальный запуск, не повторять автоматически:
& .venv-budget/Scripts/python.exe tools/compare_alternatives.py @runArgs --run
Write-Output "Exit code: $LASTEXITCODE"
```

План отдельно измеряется монотонными часами, без Ollama-запросов. Сохранённый успешный план должен соответствовать всем 30 файлам, промпту, схеме и manifest. В начале реального запуска записывается `/api/ps`: резидентная Gemma сохраняется; другие модели не выгружаются, при их наличии этот запуск отклоняется. Сервисы и глобальные настройки не меняются. Если Gemma отсутствовала, внутри времени первого кейса выполняется документированный empty-chat load, без генерации; отдельно измеряется его HTTP и доступные серверные duration. Проверяется done_reason=load, пустой ответ и отсутствие output tokens. Загрузка не вычитается из партии. Если сервер не возвращает load_duration, это явно отмечается; время всей HTTP-операции не называется чистым GPU-временем загрузки.

Полное время — от начала первого кейса до завершения `flush` + `fsync` последней строки `gemma4.jsonl`, включая проверки и межкейсную запись отчётов. Финальное создание Markdown после последней строки вне интервала. В `batch-state.json` сохранены границы каждого кейса по монотонным часам; у записей JSONL есть порядок, HTTP, Ollama load/prompt_eval/eval/total, токены и сведения `/api/ps`. Render-only учитывается отдельно: он тоже может загружать runner. GPU-проверка использует reported size_vram==size, без независимого аудита MoE-резидентности.

`comparison.md` показывает все исходные письма с разделением предыстории/периода рядом с фактическими ответами и источниками. `benchmark.md` содержит медиану, p95 nearest-rank, полное время, фактические успешные обзоры/час, ошибки и линейные оценки 1000/3000/5000 клиентов только при 30 успешных ответах. Отдельно показан подтверждённый тёплый участок; выводы об ускорении от параллелизма не делаются. Частичный прогон явно отмечается. Результаты в `evaluation/reports` исключены из Git. Синтетические объёмы и структурная корректность не доказывают смысловое качество на реальной почте.

Локальные проверки: `go test ./...`, `go vet ./...`, `go build ./...`, `.venv-budget/Scripts/python.exe -m unittest tools/test_compare_alternatives.py`. HTTP mock проверяет транспорт, параметры и отдельные load metrics; управляемые часы — включение записи и межкейсных затрат, границы тёплого участка и сохранение частичных результатов. Эти тесты не измеряют скорость реальной модели.
