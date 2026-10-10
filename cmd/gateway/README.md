# Сервис Gateway

Gateway — публичная точка входа по HTTP. Он отвечает за маршруты, идентификаторы запросов, промежуточные обработчики и преобразование ошибок gRPC в HTTP. Gateway не обращается к PostgreSQL или Redis напрямую. Auth проверяет сеансы, User обрабатывает учётные записи, Media предоставляет работу с файлами. Gateway вызывает эти сервисы по gRPC.

## Конфигурация и локальный запуск

Путь к конфигурации по умолчанию — `/etc/app/config.yaml`; изменить его можно флагом `-config`. В `config.example.yaml` указаны хосты и порты HTTP и внутренних gRPC-сервисов.

Запустите весь проект из корня репозитория (PostgreSQL, Redis, SeaweedFS, Mailpit, Migrator, Auth, User, Mail, Media и Gateway):

```sh
docker compose up --build
```

Gateway доступен по адресу `http://localhost:8080`. Для остановки нажмите `Ctrl+C`; команда `docker compose down` остановит и удалит контейнеры. Данные PostgreSQL сохранятся в именованном томе. Compose использует файлы `config.compose.yaml` с DNS-именами сервисов Docker; конфигурации для `go run` используют `localhost`.

Интерактивное описание реализованного HTTP API и примеры запросов доступны в [Scalar](../../api/http/README.md) по адресу `http://localhost:8084` после запуска Compose.
Для интерактивных запросов из Scalar локальная конфигурация разрешает CORS с адреса документации; в `config.example.yaml` список разрешённых источников пуст.

Чтобы запускать Auth, User и Gateway напрямую, сначала поднимите зависимости, Mail и Media через Compose:

```sh
docker compose up -d postgres redis mailpit mail media
```

Затем запустите каждый сервис в отдельном терминале:

```powershell
$env:REDIS_PASSWORD = 'matchlab_redis_local'
go run ./cmd/auth -config cmd/auth/config.example.yaml
```

```powershell
$env:POSTGRES_PASSWORD = 'matchlab_local_only'
go run ./cmd/user -config cmd/user/config.example.yaml
```

```sh
go run ./cmd/gateway -config cmd/gateway/config.example.yaml
```

Учётные данные Compose предназначены только для локальной разработки. Данные PostgreSQL хранятся в именованном томе. У Redis тома нет, поэтому при его перезапуске сеансы удаляются.

Media автоматически поднимает SeaweedFS, init-джобу и Migrator как зависимости.
Дождитесь его готовности. Если миграция 00002 уже применена без `is_public`,
сначала обновите локальную БД по [инструкции Migrator](../migrator/README.md).
Для запуска Media тоже напрямую используйте [его инструкцию](../media/README.md).
Не запускайте контейнер и локальный процесс одного сервиса на одном порту.
В Bash вместо `$env:NAME = 'value'` используется `export NAME=value`.

## HTTP API

Удаление готового файла: `DELETE /api/v1/media/files/{id}` с Bearer-токеном владельца
и подтверждённой почтой. Успех и повтор владельцем возвращают `204`; чужой файл,
включая публичный, возвращает `404`. Для `pending` и `failed` — `409`.
Media удаляет объект из S3, сохраняет `deleted` в БД и скрывает файл из чтения.
При сбое операция сохраняется в `deleting` и повторяется фоновой задачей.

- `POST /api/v1/auth/register`: JSON `{ "email": "student@hse.ru", "password": "..." }`; возвращает `201` и `{ "status": "pending" }` после принятия письма SMTP. Аккаунта и сеанса ещё нет. Повтор с тем же email и выбранным паролем заменяет заявку и отправляет новую ссылку; старые ссылки недействительны. Для существующего аккаунта — `409`.
- `POST /api/v1/auth/login`: тот же JSON; возвращает `user_id` и `access_token` только после подтверждения почты. До подтверждения аккаунта нет, поэтому вход возвращает `401`.
- `POST /api/v1/auth/email/confirm`: публичный метод, JSON `{ "token": "..." }`; создаёт аккаунт из актуальной заявки, успех — `204`, истёкший токен — `410`, использованный — `404`.
- `GET /api/v1/users/me/email`: требует сеанс и подтверждённую почту; возвращает состояние адреса. Смена почты не поддерживается.
- `GET /api/v1/sessions/current`: требует Bearer-токен и подтверждённую почту, возвращает `user_id`.
- `DELETE /api/v1/sessions/current`: отзывает Bearer-токен, возвращает `204`; отзыв доступен и для старого неподтверждённого сеанса.
- `GET /health`: проверка процесса.

Маршруты файлов и примеры полного сценария описаны в [README HTTP API](../../api/http/README.md):

| HTTP | RPC Media | Доступ |
| --- | --- | --- |
| `POST /api/v1/media/files` | `CreateUpload` | Сеанс и подтверждённая почта |
| `POST /api/v1/media/files/{id}/complete` | `CompleteUpload` | Владелец с подтверждённой почтой |
| `GET /api/v1/media/files/{id}` | `GetFile` | Владелец или любой читатель публичного файла |
| `GET /api/v1/media/files/{id}/download-url` | `CreateDownloadURL` | Те же права; файл должен быть ready |

Настройка `media.host`/`media.port` обязательна: локально `localhost:8085`,
в Compose `media:8085`. При прямом запуске также запустите Media по его
[инструкции](../media/README.md). Gateway ждёт подключения к Media при старте;
вызовы Media ограничены 10 секундами и передают контекст HTTP-запроса.
Если Authorization передан в запросе чтения, проверяются сеанс и почта;
без заголовка запрос гостевой. Пользовательский `user_id` не принимается.
Ответы Media не кешируются. Данные файла проходят напрямую через S3.

Для multipart-загрузки доступны `POST /api/v1/media/files/multipart`,
`GET` и `DELETE /api/v1/media/files/{id}/multipart`,
`POST /api/v1/media/files/{id}/multipart/part-urls` и
`POST /api/v1/media/files/{id}/multipart/complete`. Все требуют сеанс владельца
и подтверждённую почту, включая GET публичной загрузки. Завершение принимает
JSON до 1 МиБ со списком номеров и ETag частей; остальные JSON-запросы ограничены
16 КиБ. Контракты и пример — в [README HTTP API](../../api/http/README.md).

Фронтенд после регистрации показывает «Перейдите по ссылке в письме и подтвердите регистрацию». Mail добавляет токен в URL из `smtp.verification_url` (локально `http://localhost:5173/verify-email`). На этой странице фронтенд читает `token` из query string, вызывает API подтверждения и после `204` показывает «Адрес подтверждён» с переходом ко входу. Страница фронтенда находится в отдельном приложении. В этом репозитории реализован API для неё.

Регистрация и подтверждение не требуют сеанса. `AuthenticatedSubrouter` проверяет сеанс и email через User, включая сеансы, выданные до обновления. Публичные группы маршрутов сохраняют анонимный доступ. Все ответы содержат `X-Request-ID`; пароли и токены не записываются в журнал. Тестовые письма доступны в Mailpit: `http://localhost:8025`.

Проверка: `go test ./...`. Сборка образа: `docker build -f cmd/gateway/Dockerfile -t gateway:local .`.
