# Removed Messages Bot: краткое описание

## Что делает бот

Бот работает с Telegram Business updates и помогает владельцу видеть удаленные и измененные сообщения.

Главная идея: SQLite не является хранилищем содержимого. Тексты, caption, медиа и metadata-сообщения хранятся в отдельном Telegram archive chat. SQLite хранит только технические связи и состояние обработки.

## Основной сценарий

1. Пользователь отправляет сообщение в Telegram Business чат.
2. Бот получает `business_message`.
3. Бот определяет тип сообщения.
4. Бот создает новую запись в SQLite (`archived_messages`).
5. Бот создает запись `message_versions` (с `version_no=1` и `archive_message_id=NULL`).
6. Бот создает запись `archive_copies` со статусом `pending` (outbox).
7. Бот отправляет копию или metadata-представление в archive chat.
8. При успешном ответе Telegram бот переводит запись `archive_copies` в `sent` и обновляет `archive_message_id` в `message_versions`/`archived_messages`.
9. При ошибке Telegram запись `archive_copies` помечается как `failed` с текстом ошибки.

SQLite хранит:

- `business_connection_id`, потому что он нужен Telegram API для обработки business updates и `/send`;
- source chat id;
- source message id;
- archive chat id;
- archive message id;
- version number;
- content type;
- `media_group_id` (для альбомов);
- timestamps;
- retention expiration;
- состояние delete/edit обработки;
- статус и ошибку отправки в archive chat (outbox);
- минимальные sender identity поля для уведомлений и поиска получателя;
- `source_username_lc` (нижний регистр username) для индексированного поиска без `LOWER`/`TRIM` в WHERE.

SQLite не хранит:

- полный текст сообщения;
- text preview;
- caption;
- caption preview;
- file id;
- file unique id;
- media metadata JSON;
- raw Telegram update JSON;
- локальные файлы медиа;
- bot token.

## Удаление сообщения

1. Telegram присылает `deleted_business_messages`.
2. Бот ищет в SQLite связь исходного сообщения с archive message id.
3. Бот отправляет владельцу уведомление об удалении.
4. Если архивная копия есть, бот копирует ее владельцу из archive chat.
5. Если у удаленного сообщения был `media_group_id`, бот дополнительно копирует владельцу архивные копии остальных элементов альбома.
6. SQLite фиксирует, что delete обработан.

Если delete пришел раньше исходного сообщения, событие попадает в `pending_deletes`. Фоновый цикл повторяет обработку, пока исходная запись не появится или pending-событие не устареет.

## Редактирование сообщения

1. Telegram присылает `edited_business_message`.
2. Бот находит текущую запись в SQLite.
3. Бот сравнивает `edit_date` с `edit_date` последней версии. При совпадении событие игнорируется (idempotency для повторных доставок update'ов).
4. Бот атомарно создает следующую версию: `INSERT ... SELECT COALESCE(MAX(version_no), 0)+1 ... RETURNING id, version_no` — без race на `version_no`.
5. Бот создает pending-запись в `archive_copies`.
6. Бот отправляет новую версию в archive chat.
7. На успехе обновляются `archive_copies.send_status='sent'`, `message_versions.archive_message_id` и текущая запись `archived_messages` (через `UpdateCurrentFromVersion`).
8. На ошибке `archive_copies` помечается как `failed`, текущая запись parent не меняется.
9. Бот уведомляет владельца о редактировании.
10. Бот копирует владельцу предыдущую архивную версию.

Прошлая версия берется из archive chat, а не из SQLite.

## Business connections (подключение бота к Telegram Business)

Бот слушает Telegram-апдейт `business_connection`. Каждый такой апдейт сохраняется в таблицу `business_connections`:

- `business_connection_id` (PK);
- `owner_user_id`, `owner_user_chat_id`, `owner_username`, `owner_display_name` — кому подключён бот;
- `is_enabled` — активна ли коннекция сейчас;
- `can_reply` — может ли бот отправлять сообщения от имени бизнеса (из `BusinessBotRights.can_reply`);
- `connected_at`, `updated_at`, `disconnected_at`.

При первом получении `business_connection` или при изменении `is_enabled`/`can_reply` владелец получает уведомление в личку:

- `Business connection established` — впервые подключили;
- `Business connection re-enabled` — повторно включили;
- `Business connection disabled` — отключили (бот сохранит запись и пометит `disconnected_at`);
- `Business connection rights changed` — изменился `can_reply`.

При получении `business_message` бот сверяет `business_connection_id` с таблицей: если коннекция помечена как disabled, в логах появляется warning. Сообщение всё равно архивируется (fail-open), чтобы не терять контент при гонке порядка апдейтов.

## Команда `/connections`

Владелец из приватного чата с ботом может написать `/connections`. Бот возвращает список всех известных коннекций (ID, username владельца, состояние enabled/disabled, can_reply, время последнего обновления). Это основной способ убедиться, что бот действительно подключён и к кому именно.

## Команда `/send`

Владелец может написать в **личке с ботом**:

```text
/send [@username, @username] [message text]
```

Бот:

1. Проверяет, что сообщение пришло из приватного чата с владельцем (`message.Chat.ID == OWNER_CHAT_ID` И `message.From.ID == OWNER_CHAT_ID`). Запросы из групп блокируются, даже если отправитель — владелец.
2. Нормализует username получателей (нижний регистр).
3. Ищет business target mapping в SQLite.
4. Проверяет `business_connections`: коннекция должна быть известна, `is_enabled=true` и `can_reply=true`.
5. Отправляет business message найденным пользователям только через активные коннекции с правом ответа.
6. Возвращает summary: success, unknown, failed. Если коннекция отключена, неизвестна или без `can_reply`, получатель попадает в failed с конкретной причиной.

Пользователь должен хотя бы раз написать в business-чат, чтобы бот узнал его target chat id и `business_connection_id`.

## Какие типы сообщений обрабатываются

Нативно как медиа копируются:

- photo;
- voice;
- audio;
- document;
- video;
- animation;
- sticker;
- video note.

Как текст или metadata-сообщение архивируются:

- text;
- contact;
- location;
- venue;
- poll;
- dice;
- paid media;
- story;
- checklist;
- game;
- invoice/payment/refund;
- gifts/giveaways;
- forum/service events;
- shared users/chats;
- web app data;
- voice chat events;
- unknown fallback.

Для неизвестного типа бот сохраняет в archive chat безопасный metadata envelope без `business_connection_id`. Raw update в SQLite не пишется.

## Альбомы (media groups)

Альбом — это N сообщений с одинаковым `media_group_id`. Telegram доставляет каждый элемент отдельным `business_message` update'ом. Бот:

- сохраняет `media_group_id` в `archived_messages`;
- архивирует каждый элемент независимо (по существующей логике);
- буферизует факт прихода в in-memory map для логирования "альбом завершен";
- при удалении одного элемента альбома, если включен `RESEND_ARCHIVED_COPY_ON_DELETE`, копирует владельцу архивные копии всех соседей по `media_group_id` — чтобы владелец увидел весь альбом, а не одну фотографию.

## Retention и очистка

Retention управляется переменными:

- `RETENTION_AUDIO_HOURS`;
- `RETENTION_PHOTO_HOURS`;
- `RETENTION_TEXT_HOURS`;
- `RETENTION_OTHER_MEDIA_HOURS`;
- `CLEANUP_INTERVAL_MINUTES`;
- `DELETE_EXPIRED_FROM_ARCHIVE`.

Cleanup удаляет устаревшие archive messages и соответствующие строки SQLite. Если у сообщения было несколько версий, удаляются все известные archive copies и metadata messages. Между удалениями делается throttle ~35мс, чтобы не упереться в rate limit Telegram.

## Telegram API: retry и rate limits

Все вызовы Telegram API (Send/Copy/Delete/...) проходят через единую обертку `callWithRetry` в `internal/telegram/client.go`. Поведение:

- На 429 (`retry after N`) ждет N секунд (clamp 0..30s) и повторяет.
- На сетевые/серверные ошибки делает экспоненциальный backoff (`baseDelay * 2^attempt`, clamp до 30s).
- Постоянные ошибки (Forbidden, Unauthorized, BadRequest, missing-message) НЕ ретраятся.
- Максимум 4 попытки.

## Outbox и устойчивость к падениям

Запись в `archive_copies` теперь работает как outbox:

- pending row создается ДО отправки;
- после успешной отправки row обновляется на sent с `archive_message_id`;
- при ошибке row помечается failed;
- после рестарта pending row остается в SQLite — это маркер "бот мог отправить в archive chat, но мы не знаем результат". Сейчас это логируется; в будущем можно подключить recovery-цикл `ListPendingArchiveCopiesOlderThan`, который попытается переотправить или хотя бы предупредить владельца о возможном orphan-сообщении в archive chat.

## Schema migrations

Бот создает таблицу `schema_migrations` и записывает в нее `(version, applied_at)` после каждой успешной миграции. Текущая `currentSchemaVersion = 2`. Когда понадобится breaking-миграция, добавляется новая ветка с проверкой `MAX(version)`.

## Миграция старой локальной БД

Старые тестовые content-колонки больше не используются и не остаются в целевой схеме. При `Migrate` SQLite-таблицы пересобираются без legacy-полей:

- `archived_messages.file_id`;
- `archived_messages.file_unique_id`;
- `archived_messages.text_preview`;
- `archived_messages.caption`;
- `message_versions.text_full`;
- `message_versions.text_preview`;
- `message_versions.caption_full`;
- `message_versions.caption`;
- `message_versions.file_id`;
- `message_versions.file_unique_id`;
- `message_versions.metadata_json`;
- лишние identity-поля в `business_targets`.

После пересборки выполняется `VACUUM` и WAL checkpoint, чтобы старое содержимое не оставалось в свободных страницах SQLite-файла. Новые колонки (`source_username_lc`, `media_group_id` в `archived_messages`; `edit_date` в `message_versions`) добавляются через ALTER TABLE при апгрейде.
