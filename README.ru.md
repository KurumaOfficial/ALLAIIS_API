<h1 align="center">ALLAIIS API</h1>

<p align="center"><strong>Один локальный ключ для выбранных AI API.</strong></p>

<p align="center">
  <a href="go.mod"><img alt="Go 1.26.2+" src="https://img.shields.io/badge/Go-1.26.2%2B-00ADD8?logo=go"></a>
  <a href="LICENSE"><img alt="GPL-3.0-only" src="https://img.shields.io/badge/license-GPL--3.0--only-4158A6"></a>
  <a href="../../actions/workflows/ci.yml"><img alt="CI" src="../../actions/workflows/ci.yml/badge.svg"></a>
</p>

<p align="center"><a href="README.md">English</a> · <strong>Русский</strong></p>

[Быстрый старт](#быстрый-старт) · [API](#api) · [Провайдеры](#провайдеры) · [Статус](#статус) · [Запуск у себя](docs/runbook.ru.md) · [Блокеры интеграции](docs/BLOCKED.md)

ALLAIIS — локальный шлюз на Go для AI-клиентов, включая OpenCode. Регистрируй алиасы моделей, храни ключи провайдеров в зашифрованном хранилище и используй один loopback-адрес для JSON и потоковых ответов.

## Панель управления

```powershell
.\allaiis.exe gui
```

`gui` поднимает гейтвей и открывает панель, доступную только с loopback: в ней
можно добавлять ключи, захватить веб-сессию Venice, регистрировать алиасы,
смотреть каталоги, отправлять пробы, запускать диагностику и наблюдать живой
лог. Панель слушает `127.0.0.1` на случайном порту и требует одноразовый токен;
наружу ничего не уходит. Изменения применяются к работающему гейтвею за секунды
через горячую перезагрузку — без рестарта. Ту же диагностику без браузера даёт
`allaiis doctor`, а `allaiis smoke` прогоняет каждый алиас через временный
внутренний гейтвей — CI ловит сломанный маршрут, не трогая рабочий. `allaiis
tools show` и `allaiis tools reset` читают и очищают запись имён инструментов,
по которой предупреждает врач. См.
[docs/gui.ru.md](docs/gui.ru.md). Слои кеша, инвалидация, повторы и коды ошибок
описаны в [docs/quality.ru.md](docs/quality.ru.md).

## Быстрый старт

**Локальный запуск проверен офлайн. Записанных live-проверок провайдеров: 1 (Venice web).** Тест использует сервер на loopback и тестовые ключи.

| Шаг | Статус |
| --- | --- |
| Открыть локальную копию с Go 1.26.2 или новее | Предварительное условие |
| Создать хранилище, импортировать ключ и зарегистрировать алиас | Проверено офлайн в `TestQuickStartOffline` |
| Получить локальный ключ и запустить шлюз | Проверено офлайн в `TestQuickStartOffline` |
| Отправить запросы с JSON и SSE | Проверено офлайн в `TestQuickStartOffline` |

```sh
go test -count=1 -v -run '^TestQuickStartOffline$' ./core/cmd/allaiis
```

### После блока B

**Живой прогон ожидает проверки.** Команды ниже предназначены для будущего прогона через официальный API DeepSeek. [Статус интеграции](docs/BLOCKED.md)

| Шаг | Офлайн-проверка | Live-статус |
| --- | --- | --- |
| Клонировать репозиторий и собрать CLI | Сборка проверена командой `go build ./...` | Локальная настройка |
| Задать `DEEPSEEK_API_KEY` и выбрать `DEEPSEEK_MODEL_ID` | Реальный ключ и выбор в каталоге не покрыты | Ожидает проверки |
| Выполнить `init`, `import` и `models add` | `TestQuickStartOffline`, тестовый провайдер | Ожидает проверки |
| Получить ключ шлюза и выполнить `up` | `TestQuickStartOffline`, локальный сервер | Ожидает проверки |
| Отправить запрос Chat Completions | `TestQuickStartOffline`, фикстуры JSON и SSE | Ожидает проверки |

Задай в `DEEPSEEK_MODEL_ID` точный ID модели, доступной в твоём аккаунте DeepSeek. Пример для Linux и macOS использует `deepseek-flash` по умолчанию.

<details open>
<summary>Linux и macOS</summary>

```sh
git clone https://github.com/KurumaOfficial/ALLAIIS_API.git
cd ALLAIIS_API
go build -o allaiis ./core/cmd/allaiis
./allaiis init
./allaiis import --from env --provider deepseek
./allaiis models add --provider deepseek --upstream-model "${DEEPSEEK_MODEL_ID:-deepseek-flash}" --name flash
export ALLAIIS_API_KEY="$(./allaiis key)"
./allaiis up
```

</details>

<details>
<summary>Windows PowerShell</summary>

```powershell
git clone https://github.com/KurumaOfficial/ALLAIIS_API.git
cd ALLAIIS_API
go build -o allaiis.exe ./core/cmd/allaiis
.\allaiis.exe init
.\allaiis.exe import --from env --provider deepseek
.\allaiis.exe models add --provider deepseek --upstream-model $env:DEEPSEEK_MODEL_ID --name flash
$env:ALLAIIS_API_KEY = .\allaiis.exe key
.\allaiis.exe up
```

</details>

В клиенте укажи базовый URL `http://127.0.0.1:8420/v1`, передай локальный `ALLAIIS_API_KEY` и выбери `flash-deepseek`. Команда `allaiis up` занимает текущий терминал. В другом терминале получи тот же ключ командой `allaiis key`.

<details>
<summary>Отправить тестовый запрос</summary>

```sh
curl --no-buffer http://127.0.0.1:8420/v1/chat/completions \
  -H "Authorization: Bearer $ALLAIIS_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"flash-deepseek","messages":[{"role":"user","content":"Поздоровайся."}],"stream":true}'
```

```powershell
$body = @{
  model = 'flash-deepseek'
  messages = @(@{ role = 'user'; content = 'Поздоровайся.' })
} | ConvertTo-Json -Depth 5
Invoke-RestMethod http://127.0.0.1:8420/v1/chat/completions -Method Post `
  -Headers @{ Authorization = "Bearer $env:ALLAIIS_API_KEY" } `
  -ContentType 'application/json' -Body $body
```

</details>

## API

| Эндпоинт | Назначение |
| --- | --- |
| `GET /healthz` | Проверка доступности локального шлюза |
| `OPTIONS /v1/*` | CORS-preflight без ключа |
| `GET /metrics` | Счётчики Prometheus, без ключа, loopback |
| `GET /v1/models` | Алиасы, доступные по настроенной политике приватности |
| `POST /v1/chat/completions` | Chat Completions с нативным JSON или SSE |
| `POST /v1/responses` | Создание Responses в синхронном режиме с JSON или SSE |
| `POST /v1/messages` | Создание Anthropic Messages с нативным JSON или SSE, в том числе на веб-маршрутах |

Каждый алиас обслуживает один эндпоинт генерации. По умолчанию `models add` выбирает протокол `chat`. `GET /v1/models` помечает алиасы Responses и Messages полем `allaiis_protocol`. Шлюз передаёт нативные вызовы инструментов и статистику токенов, если их возвращает настроенный API.

<details>
<summary>Настроить Responses и Messages</summary>

Задай `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_MODEL_ID` и `ANTHROPIC_MODEL_ID` в терминале.

```sh
./allaiis account add --provider openai --label work --key-env OPENAI_API_KEY
./allaiis account add --provider anthropic --label work --key-env ANTHROPIC_API_KEY
./allaiis models add --provider openai --upstream-model "$OPENAI_MODEL_ID" --name responses --protocol responses --label work
./allaiis models add --provider anthropic --upstream-model "$ANTHROPIC_MODEL_ID" --name messages --protocol messages --label work
```

Перезапусти шлюз после изменения конфигурации. Отправь `{"model":"responses-openai","input":"Say hello."}` на `/v1/responses`. Алиас Responses требует метку ключа. Для продолжения через `previous_response_id` используй тот же алиас и проект провайдера. [OpenAI Responses](https://developers.openai.com/api/reference/resources/responses/methods/create)

Отправь `{"model":"messages-anthropic","max_tokens":32,"messages":[{"role":"user","content":"Say hello."}]}` на `/v1/messages`. Локальный ключ передаётся в `x-api-key` или `Authorization: Bearer`. ALLAIIS передаёт `Anthropic-Version` и `Anthropic-Beta`. Версия по умолчанию — `2026-10-21`. [Claude Messages](https://platform.claude.com/docs/en/api/messages/create)

</details>

## Провайдеры

Маршруты работают либо на официальном API-ключе, либо на **веб-сессии** (Тир C). Веб-маршруты используют тот же локальный ключ, те же алиасы и тот же OpenAI-контракт; отличаются только транспорт и квота. Venice — первый живой веб-транспорт: [досье Venice web](docs/venice-web.md).

```sh
./allaiis web chrome                                  # одноразовый браузер для входа
./allaiis web login --label personal                  # захват куки в хранилище
./allaiis models discover --provider venice-web --match uncensored
./allaiis models add --provider venice-web --upstream-model venice-uncensored-1-2 \
  --name uncensored --privacy zdr
```

`web login` без `--cookie` читает Chrome на `127.0.0.1:9222`; передай `--cookie "cf_clearance=…; __client=…"`, чтобы захватить из любого другого браузера. `./allaiis web quota` показывает остаток веб-квоты. Веб-ответы помечены `X-ALLAIIS-Transport-Tier: tier_c3` и `X-ALLAIIS-Emulated-Features: usage_estimate`, а токены в них оценочные.


| ID провайдера | Переменная с API-ключом | Документация |
| --- | --- | --- |
| `openai` | `OPENAI_API_KEY` | [OpenAI](https://platform.openai.com/docs/api-reference/chat) |
| `deepseek` | `DEEPSEEK_API_KEY` | [DeepSeek](https://api-docs.deepseek.com/) |
| `zai` | `ZAI_API_KEY` | [Z.ai](https://docs.z.ai/api-reference/llm/chat-completion) |
| `venice` | `VENICE_API_KEY` | [Venice](https://docs.venice.ai/api-reference/endpoint/chat/completions) |
| `venice-web` | веб-сессия | [Venice web](docs/venice-web.md) |
| `qwen` | `DASHSCOPE_API_KEY` | [Alibaba Cloud](https://help.aliyun.com/en/model-studio/compatibility-of-openai-with-dashscope) |
| `gemini` | `GEMINI_API_KEY` | [Gemini](https://ai.google.dev/gemini-api/docs/openai) |
| `anthropic` | `ANTHROPIC_API_KEY` | [Claude](https://platform.claude.com/docs/en/api/openai-sdk) |
| `arena-ai` | `ARENA_API_KEY` | [Arena](https://platform.arena.ai/docs/api) |
| `opencode` | `OPENCODE_API_KEY` | [OpenCode Console](https://opencode.ai/docs/console/harnesses#base-urls) |
| `opencode-go` | `OPENCODE_API_KEY` | [OpenCode Go](https://opencode.ai/docs/console/go/#where-can-i-use-it) |

```sh
./allaiis models discover --all
./allaiis models discover --all --with-keys
./allaiis models discover --provider venice --match 'DeepSeek'
./allaiis models discover --provider opencode-go --label workspace
```

Поиск возвращает результаты по провайдерам. Публичные каталоги доступны до `init`. Флаг `--with-keys` подключает сохранённые ключи при запросе нескольких каталогов. Флаг `--label` выбирает ключ для одного провайдера. Модель с поддержкой чата регистрируется отдельно через `models add`.

<details>
<summary>Подключить одну модель через Venice и OpenCode Go</summary>

```sh
./allaiis models discover --provider venice --match 'DeepSeek V4.1 Flash'
./allaiis models discover --provider opencode-go --match 'DeepSeek V4.1 Flash'
./allaiis account add --provider venice --label personal
./allaiis account add --provider opencode-go --label workspace
./allaiis models add --provider venice --upstream-model "$VENICE_MODEL_ID" --name deepseek-flash --label personal
./allaiis models add --provider opencode-go --upstream-model "$OPENCODE_MODEL_ID" --name deepseek-flash --label workspace
```

Перед регистрацией алиасов задай `VENICE_MODEL_ID` и `OPENCODE_MODEL_ID` по результатам поиска. Выбирай `deepseek-flash-venice` или `deepseek-flash-opencode-go` с одним локальным ключом. OpenCode Go предназначен для агентов разработки. Доступ к моделям с ограничениями зависит от workspace. [Доступ к моделям OpenCode Go](https://opencode.ai/docs/console/go/#enable-restricted-models)

</details>

<details>
<summary>Свой провайдер и региональные адреса</summary>

Используй HTTPS или числовой loopback-адрес для HTTP. Поле `base_url` задаёт префикс перед последней частью пути API. Пример `config.json` для совместимого локального сервера.

```json
{
  "providers": {
    "local": {
      "base_url": "http://127.0.0.1:11434/v1",
      "key_env": "LOCAL_LLM_API_KEY"
    }
  },
  "models": [
    { "id": "local-model", "provider": "local", "upstream_model": "your-installed-model" }
  ]
}
```

Импортируй ключ перед использованием маршрута. Для локального сервера, который игнорирует авторизацию, можно задать отдельный ключ-заглушку. Каталог по умолчанию доступен на `<base_url>/models`. Для публичного каталога укажи `"catalog_public": true`. Региональный Qwen с адресом, оканчивающимся на `/compatible-mode/v1`, поддерживает постраничный поиск через `"catalog_format": "dashscope"`. [Каталог Alibaba Cloud](https://help.aliyun.com/en/model-studio/list-models)

</details>

## Ключи и OpenCode

```sh
./allaiis import --from opencode --path /path/to/opencode/auth.json
./allaiis account add --provider deepseek --label work --key-env WORK_DEEPSEEK_API_KEY
./allaiis status
./allaiis export opencode
```

Добавь экспортированного провайдера в `opencode.json`, передай `ALLAIIS_API_KEY` процессу OpenCode и выбери `allaiis/<model-alias>`. Экспорт включает алиасы Chat Completions. [Конфигурация провайдеров OpenCode](https://opencode.ai/docs/providers/)

- **Хранилище.** Выбери пароль длиной от 16 байт. Для запуска без ввода задай `ALLAIIS_VAULT_PASSPHRASE` в окружении процесса. Данные в SQLite зашифрованы AES-256-GCM. Ключ шифрования выводится через Argon2id.
- **Каталог данных.** Переменная `ALLAIIS_DATA_DIR` переопределяет путь. По умолчанию используются `%APPDATA%\allaiis` на Windows, `~/Library/Application Support/allaiis` на macOS и `${XDG_CONFIG_HOME:-~/.config}/allaiis` на Linux.
- **Пулы ключей.** Пул привязан к ID и базовому URL провайдера. Флаг `--label` закрепляет маршрут за ключом. Удаление выполняется командой `allaiis account remove --provider <provider> --label <label>`. После изменения настроек или ключей перезапусти шлюз.
- **Маршрутизация.** `X-ALLAIIS-Session` и `X-OpenCode-Session` помогают сохранять выбор ключа. `X-ALLAIIS-Min-Privacy` поднимает минимальный уровень приватности. `429` приостанавливает метку на время `Retry-After`, `402` паркует только эту модель на метке, а пул без привязки повторяет запрос на другой метке, пока клиент ничего не увидел. Максимальный размер запроса — 8 МиБ.
- **Импорт OpenCode.** CLI импортирует распознанные записи типа `api` из `auth.json`. По умолчанию проверяются `$XDG_DATA_HOME/opencode/auth.json` и `~/.local/share/opencode/auth.json`.

## Статус

**Записанных live-проверок для этой матрицы: 1 (Venice web).**

| Возможность | Реализовано | Офлайн-тест | Live-тест | Блокер |
| --- | :---: | --- | :---: | --- |
| Маршрутизация, приватность и пауза после 429 | ✓ | `TestRoutingAndProviderCooldown` | — | — |
| Кулдаун привязан к метке ключа | ✓ | `TestCooldownIsScopedToThePinnedLabel`, `TestCooldownOnUnpinnedPoolCoolsEveryLabel` | гость работал, пока Pro остывал | — |
| Снятие кулдауна из панели | ✓ | `TestClearCooldownEndpoint`, `TestClearCooldownLiftsModelMarksToo` | — | — |
| Пул без привязки повторяет запрос на сбойной метке до первого байта | ✓ | `TestUnpinnedPoolRetriesAnotherLabel`, `TestPoolSkipsACooledLabel` | метка с 429 запаркована, ответила следующая | — |
| Пустой баланс паркует модель, а не метку | ✓ | `TestBalanceCooldownNamesOneModel`, `TestBalanceMarkNamesOneModel`, `TestBalanceMarkMovesTheModelToAnotherLabel`, `TestPanelShowsABalanceMark` | помеченная модель `429`, DeepSeek `200` на той же метке | — |
| Doctor называет запаркованные метки и срок | ✓ | `TestCooldownVerdict` | проверка `credential cooldowns` на запаркованном госте | — |
| Вызов без аргумента ремонтируется и после текста | ✓ | `TestWebCallWithoutArgumentsAfterProseIsRepaired` | два прогона агента: 0 ошибок инструментов (22 шага/30 вызовов, 6 шагов/7 вызовов) | — |
| Классификация `insufficientBalance` | ✓ | `TestErrorClassification` | 402 от исчерпанного Pro-плана стал `insufficient_balance` | — |
| Проба квоты в панели | ✓ | `TestErrorClassification` | `web quota` на трёх метках, свои времена сброса | — |
| Нативные Responses и Messages | ✓ | `TestNativeProtocols` | — | — |
| SSE и отмена при отключении клиента | ✓ | `TestStreamFlushAndCancel` | — | — |
| Закрытая веб-интеграция Venice | ✓ | `TestChatStreamsAndAuthenticates`, `TestWebCompletionStreams` | JSON и SSE | — |
| Проба квоты и каталог Venice web | ✓ | `TestErrorClassification`, `TestCatalogAcceptsDateRetiringField` | `web login`, `models discover` по живому каталогу | — |
| Pro-сессия Venice web | ✓ | `TestClerkExpiryAndForget`, `TestExpiredSignedSessionIsUnauthorized` | Opus 5.5, GPT-6.2 Astra Pro | — |
| Гостевой и Pro алиасы рядом | ✓ | `TestProviderAliasesAndPinnedCredentials` | гость + Pro в одном прогоне | — |
| `min_p` и контроль anonymous-id | ✓ | `TestMinPFollowsTheCapability`, `TestPayloadMinPAndAnonUserID` | — | — |
| Лимит видео-вложений | ✓ | `TestAttachmentCountsSplitByMIME`, `TestVideoLimitRejectsBeforeUpload` | — | — |
| Мышление в оценке usage | ✓ | `TestReasoningCountsTowardTheCap` | — | — |
| Отказ по лимиту ограничен десятью минутами | ✓ | `TestHugeRetryAfterIsCapped`, `TestHugeRateLimitResetIsCapped` | ответ с `Retry-After: 62088` сброшен, алиас ответил сразу | — |
| Ключ повтора совпадает с пересобранным турном, и с текстом, и с блоком | ✓ | `TestRenderCallListCompactsArguments`, `TestReasoningReplaySurvivesClientRendering`, `TestReasoningStoreFindsBareProse` | живой прогон OpenCode: ответ подставляется на каждом шаге с размышлением | — |
| Ключ reasoning без учёта пробелов | ✓ | `TestReasoningKeyIgnoresSurroundingSpace` | живой прогон OpenCode: повтор срабатывает на каждом ходе с мышлением | — |
| Round-trip зашифрованного reasoning | ✓ | `TestReasoningIsReplayedWithFollowUps`, `TestNonStreamRepliesCarryReasoning` | вживую: `reasoning_content`, затем повтор мышления на бесплатной думающей модели | — |
| Сэмплинг, штрафы и подсказка замены | ✓ | `TestSamplingAndPenaltiesFollowCapabilities`, `TestUnknownModelMentionsTheReplacement` | — | — |
| Большой контекст, строгий порядок и watchdog стрима | ✓ | `TestLargeContextReachesThePayload`, `TestStrictOrderMergesConsecutiveTurns`, `TestStalledStreamMapsToUpstreamTimeout` | дамп payload и ответ 504 | — |
| Reasoning effort, web-флаг и лимит тулов | ✓ | `TestReasoningEffortAndWebSearchReachThePayload`, `TestMessagesThinkingBudgetBecomesEffort`, `TestToolLimitRejects` | дамп payload на бесплатной модели | — |
| Возможности модели из каталога | ✓ | `TestCatalogCarriesCapabilities`, `TestCapabilitiesShapeTheRequest`, `TestContextLimitRejectsBeforeSending`, `TestAddModelStoresCapabilities` | reasoning и кламп `max_tokens` на бесплатной модели | — |
| Метрики Prometheus | ✓ | `TestMetricsExposition`, `TestMetricsNeedsNoKey` | `/metrics` на живом гейтвее | — |
| Request ID и CORS | ✓ | `TestRequestIDIsEchoedAndStamped`, `TestErrorBodyCarriesRequestID`, `TestPreflightAndCORSHeaders` | — | — |
| Инвалидация кеша при смене ключа | ✓ | `TestForgetDropsTokenAndAttestationCaches`, `TestReplacingAKeyClearsItsCooldown` | — | — |
| Кеш каталога с обходом | ✓ | `TestCatalogCacheServesAndBypasses` | кеш и обход в панели | — |
| Повтор пустого ответа и первый пустой чанк | ✓ | `TestWebEmptyFirstEventKeepsLaterText`, `TestWebEmptyReplyRetriesOnce` | — | — |
| Линейный скан guard и лимит блока | ✓ | `TestGuardScansForwardThroughLongProse`, `TestGuardRejectsOversizedUnclosedBlock` | 5/5 прогонов агента после правки | — |
| Дефолтный лимит ответа и финиш `length` | ✓ | `TestDefaultResponseCapWhenClientOmitsTokens`, `TestReplyNearTheTokenCapReportsLength` | 9/9 прогонов агента без обрыва | — |
| Закрытый блок с неизвестным инструментом доставляется | ✓ | `TestToolGuardUnwrapsStackedWrappers`, `TestToolGuardUnwrapsKeyValueWrapper`, `TestToolGuardKeepsProseInAWrapper`, `TestToolGuardDropsATrailingNameWord` | две попытки ремонта в прогоне на 12 функций | — |
| Плавная деградация на длинной задаче | ✓ | — | 12 функций: 85 шагов, 125 подтестов, 4 восстановленные ошибки инструментов, 0 утечек | — |
| Code Mode выключен для текстовых тулов: без разминки в песочнице | ✓ | `TestToolEnvelopeListsShapesAndCodeMode` | та же задача: 3 → 0 вызовов песочницы; задача на 8 функций: 44 шага, 91 зелёный подтест, 0 ошибок, 0 утечек | — |
| Шапка с формой аргументов и сандбокс-маршрутом | ✓ | `TestToolEnvelopeListsShapesAndCodeMode`, `TestToolShapeSkipsOptionalOverflow`, `TestInvokeArgumentsStringWrapperIsFlattened` | прогон OpenCode в Code Mode: задача сделана, тесты зелёные | — |
| Незакрытый DSML-блок дочитывается в конце стрима | ✓ | `TestToolGuardFlushRepairsUnclosedDSML`, `TestToolEnvelopePutsNamesFirst` | задача на 52 шага: тесты зелёные, поиск через `execute` упал с 25 до 7 | — |
| Смешанные диалекты тулов и обёртка `arguments` | ✓ | `TestToolGuardAcceptsMixedDialect`, `TestInvokeArgumentsWrapperIsFlattened` | две задачи на код: баг исправлен, тесты зелёные | — |
| Диалект тулов DeepSeek DSML и растущие индексы вызовов | ✓ | `TestToolGuardParsesDeepSeekDSML`, `TestStreamedToolCallIndexesKeepRising`, `TestWebToolCallStreamDSML`, `TestToolGuardFlushRepairsTruncatedBlock`, `TestWhitespaceAssistantTextKeepsToolCalls` | цикл агента OpenCode на `deepseek-v4-1-flash-uncensored`: 9 вызовов, 5 шагов, 16 с | — |
| Usage в стриме по `stream_options.include_usage` | ✓ | `TestChatStreamUsageOnlyWhenRequested` | OpenCode видит ненулевые токены | — |
| Эмуляция тулов Тира C (sentinel и XML) | ✓ | `TestToolGuardSplitsMarkers`, `TestToolGuardParsesInvokeXML`, `TestToolGuardDropsDuplicatedDialect`, `TestWebToolCallStream`, `TestWebToolRepairRetry` | вызов `read_file` на всех трёх протоколах | — |
| Веб-маршруты на `/v1/messages` | ✓ | `TestMessagesProtocolStreams`, `TestMessagesProtocolToolUse` | текст Opus и `tool_use` | — |
| Веб-маршруты на `/v1/responses` | ✓ | `TestResponsesFollowUpByPreviousId` (переживает новый handler) | SSE-формат, шифрованные продолжения | — |
| Локальная панель (`allaiis gui`) | ✓ | `TestPanelAddKeyAndAlias`, `TestPanelRejectsMissingToken` | добавление алиаса, проба и удаление через панель | — |
| Отключение алиаса без удаления | ✓ | `TestSetModelDisabled`, `TestToggleMutesAlias` | — | — |
| Горячая перезагрузка и наблюдатель | ✓ | `TestReloadSwapsAndKeepsPreviousOnError`, `TestWatchReloadsChangedConfig` | изменения без рестарта | — |
| Ротация ключа гейтвея | ✓ | `TestPanelDeleteAndRotate` | — | — |
| Прогон алиасов (`smoke`, панель) | ✓ | `TestSmokePassesEveryAlias`, `TestSmokeReportsFailures` | 20 подряд запусков с инструментами, все алиасы | — |
| Проверка инструментов в каждом прогоне | ✓ | `TestSmokeProbesEveryAvailableAlias`, `TestToolCallTextReadsEachProtocol`, `TestToolProbeFailsOverToAnotherAlias`, `TestProbeCallStaysOutOfTheRecord` | вызов `list_files` получен на живом алиасе | — |
| Плановый прогон и webhook-тревога | ✓ | `TestHealthReportsRegressionsOnly`, `TestWebhookDelivers`, `TestScheduledSmokeRecordsHistory` | — | — |
| Диагностика (`doctor`, панель) | ✓ | `TestDiagnosticsFlagsMissingCredential`, `TestDiagnoseReportsToolNames`, `TestDiagnosePrefersTheLiveGateway`, `TestFetchLiveStatsReadsTheEndpoint` | запуск CLI на живом хранилище и счётчиках гейтвея | — |
| Запись незнакомых имён и срок хранения | ✓ | `TestToolNameStateSurvivesARestart`, `TestLiveUnknownNamesSkipsExpired`, `TestDropExpiredToolNamesKeepsTheFresh`, `TestToolNamesEndpoint`, `TestToolsShowMarksExpiredNames` | запись переживает рестарт, пометка просрочки и удаление | — |
| Метрики запросов и живой лог | ✓ | `TestPanelProbe` | счётчики в панели | — |
| Ввод изображений и файлов | ✓ | `TestAgentBodyCarriesMediaParts`, `TestAgentBodyMarksFiles`, `TestWebVisionUsesAgentMultipart` | цвет по PNG, поле из .txt | — |
| Старт при входе (`service`) | ✓ | `TestServiceInstallWritesALoginScript` | пусковой скрипт поднял гейтвей и панель, перезапустил после убийства | — |
| Ротируемый лог | ✓ | `TestRotatingFileKeepsTheCap`, `TestRotatingFileCreatesTheDirectory` | гейтвей писал и ротировал лог | — |
| Сохранение и восстановление с хэшами | ✓ | `TestBackupAndRestoreRoundTrip`, `TestRestoreRejectsATamperedFile` | хранилище, конфиг и запись сохранены и восстановлены | — |
| Самообновление со сверкой хэша | ✓ | `TestWantedSumFindsTheFile`, `TestDownloadWritesTheBody`, `TestSwapBinaryReplacesAndKeepsTheOld` | `update --check` чисто ответил «релизов пока нет» | нужен опубликованный тег | — |
| Настраиваемый лимит тела запроса | ✓ | `TestRequestCapComesFromTheConfig`, `TestMaxRequestDefaultsAndParses`, `TestMaxRequestRange` | тело на 9 МиБ получило `request_too_large` с пределом | — |

### Офлайн-совместимость

Код проверки совместимости в `core/internal/gateway` запускается только в
Go-тестах. Он использует синтетические данные, локальные серверы и указанные
ниже тестовые лимиты.

| Возможность | Реализовано | Офлайн-тест | Live-тест | Блокер |
| --- | :---: | --- | :---: | --- |
| A1. Закрытый транспорт Venice | ✓ | `TestWebCompletionStreams`, `TestChatStreamsAndAuthenticates` | JSON и SSE | вход в Pro вручную |
| A2. Фикстуры SSE, обрыва, ошибок и отказа в доступе | ✓ | `TestSSEFixtures` | — | — |
| A3. Форматы инструментов и повтор при ошибке | ✓ | `TestToolFixtureCorpus`, `TestToolParserRecovery`, `TestToolsCancelAndRetry`, `TestReasoningBudgetCancelsAndReturnsStreamError` | — | — |
| A3b. Веб-конверт инструментов и ремонт | ✓ | `TestToolGuardRejectsBadBlocks`, `TestWebToolRepairRetry` | ответ `tool_calls` | — |
| A4–A5. Нормализация истории и удаление NUL в тестовом обработчике | ✓ | `TestToolPolicyHistoryAndNUL`, `TestUsageFromSentPayloadAndFinalSSEChunk` | — | — |
| A6. Оценка usage в тестовых ответах JSON и SSE | ✓ | `TestUsageFromSentPayloadAndFinalSSEChunk` | — | — |
| A7. Преобразование 413 переполнения и обрезка | ✓ | `TestContextOverflowTrimsOrReturns400` | — | — |
| A8. Бесплатные модели в фикстуре и отказ по tools на агентских | ✓ | `TestOfflineCatalogAndAgentToolRejection` | — | — |
| A9. Пул веб-сессий и переключение при 429 | ✓ | `TestWebCompletionErrorsAndCooldown` | гостевой пул | мультисессии Pro ожидают |
| A10. Отмена стрима шлюза и изоляция поздних ответов | ✓ | `TestStreamFlushAndCancel` | — | — |
| A11. Метаданные и ротация логов в тестовом обработчике | ✓ | `TestUsageFromSentPayloadAndFinalSSEChunk`, `TestCompletionLogRotation` | — | — |

| ID в фикстуре | Токены контекста | Токены вывода | Инструменты |
| --- | ---: | ---: | --- |
| `zai-org-glm-5.2` | 131 072 | 8 192 | Да |
| `agent-fixture.1` | 32 768 | 4 096 | Нет |

Набор фикстур форматов инструментов запускается командой
`go test -count=1 -v -run '^TestToolFixtureCorpus$' ./core/internal/gateway`.

```sh
go test ./...
go vet ./...
gofmt -l core    # ничего печатать не должен
```

CI гоняет это на Linux, macOS и Windows, плюс `-race` на Linux
(`.github/workflows/ci.yml`). Проверка документации ещё и сверяет форму каждой
двуязычной пары (заголовки, таблицы, списки, блоки кода) и то, что каждый
упомянутый тест, относительная ссылка и якорь существуют, чтобы ни язык, ни ссылка
не могли тихо разъехаться.

[Дорожная карта](ROADMAP.md) · [Связанные проекты](docs/related-projects.md)

## Лицензия

Только GNU General Public License v3.0 (`GPL-3.0-only`). [Текст лицензии](LICENSE)
