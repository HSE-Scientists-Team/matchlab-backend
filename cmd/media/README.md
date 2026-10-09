# Media

Media обслуживает внутренний gRPC API для загрузки, метаданных и скачивания файлов.
Контракт: `api/proto/media/v1/media.proto`.
Сгенерированный Go-код: `internal/gen/media/v1`.

При запуске проверяет PostgreSQL, наличие `media.file` и доступ к S3 bucket.
Регистрирует `grpc.health.v1.Health` для пустого имени и
`matchlab.media.v1.MediaService`. При SIGTERM прекращает объявлять готовность
и ожидает завершения запросов до 10 секунд. Сервер ограничивает каждый RPC
10 секундами, более короткий deadline клиента сохраняется.

## Реализовано

Модель `internal/media/domain` соответствует существующей таблице `media.file`.
PostgreSQL-репозиторий `internal/media/repository` предоставляет:

- `CreatePending`: создание записи без фактических метаданных объекта;
- `FindOwned`: чтение по ID файла и владельца;
- `FindReadable`: чтение владельцем или любым пользователем для `is_public = true`, включая гостя;
- `MarkReady`: атомарное подтверждение загрузки с фактическими метаданными.

Повторное подтверждение возвращает сохранённую запись `ready`. Другие состояния,
кроме `pending` и `ready`, подтверждать нельзя. Подтверждение чужого файла
возвращает `NotFound`, даже если файл публичный. Запросы к S3 должны
выполняться до вызова `MarkReady`.

`internal/media/usecase` реализует четыре сценария:

- `CreateUpload`: проверяет UUID, имя, тип и размер, выбирает object key,
  подписывает PUT и сохраняет `pending`; при ошибке сохранения ссылка не выдаётся;
- `GetFile`: читает метаданные владельца или публичного файла без обращения к S3;
- `CompleteUpload`: получает фактические метаданные через HEAD, проверяет
  Content-Type и максимальный размер, затем вызывает `MarkReady`;
- `CreateDownloadURL`: выдаёт GET-ссылку владельцу или любому читателю публичного файла в `ready`.

Удалённый файл возвращается как отсутствующий. Повторное подтверждение `ready`
не обращается к S3. Отсутствующий объект или временная ошибка S3 оставляют
запись в `pending` для последующего повтора.

`internal/media/storage/s3` использует AWS SDK for Go v2. Адаптер подписывает
PUT/GET, читает HEAD и проверяет существование bucket через `Check`.
Media не создаёт bucket и не управляет IAM. Используются отдельные ключи Media.

Bucket задаётся настройкой `s3.bucket`, ключ объекта —
`users/{owner_user_id}/{file_id}`, где `file_id` — случайный UUID.
Оригинальное имя хранится в PostgreSQL, а в GET-подписи передаётся через
`Content-Disposition: attachment`. Объект появляется только после PUT.
Публичные и приватные файлы используют одинаковую схему ключей.

PUT подписывает `Content-Type`, `Content-Length` и `If-None-Match: *`.
Клиент должен передавать выданные заголовки: неверный тип или размер нарушает
подпись. Повторная загрузка в существующий object key отклоняется с `412`.
Пустые файлы пока не поддерживаются: размер должен быть положительным.
В браузере `Content-Length` формируется автоматически из тела запроса;
его нельзя задавать вручную. Не используйте multipart/form-data для PUT:
телом должны быть байты самого файла.

В существующую миграцию `00002_create_media_files.sql` добавлен
`is_public boolean NOT NULL DEFAULT false`. Ожидаемый размер и срок действия загрузки
пока не сохраняются: соответствующих колонок в текущей миграции нет.
Миграции применяет только отдельная джоба Migrator.

Если миграция 00002 уже применена к локальной БД, Goose не выполнит её повторно.
Для сохранения существующих данных выполните однократно в этой БД:

```sql
ALTER TABLE media.file ADD COLUMN IF NOT EXISTS is_public boolean NOT NULL DEFAULT false;
```

В новой БД поле создаст Migrator из исправленной миграции. Тесты используют новые БД.

Ожидаемый размер связан с PUT-подписью, но не доступен для отдельного сравнения
в `CompleteUpload`. Сервис не хранит состояние загрузки в оперативной памяти.
`Content-Type` из HEAD — метаданные S3, а не проверка содержимого файла.
ETag не используется как SHA-256. Контрольная сумма сохраняется только если
S3 вернул SHA-256 всего объекта в подходящем формате.

## Конфигурация

`config.example.yaml` предназначен для прямого запуска процесса,
`config.compose.yaml` — для Compose. Настройки загружает `config.Load`.
Параметры: gRPC-адрес, PostgreSQL, внутренний и клиентский S3 endpoint,
bucket, регион, path-style, тайм-аут, разрешённые типы, лимит размера и TTL.

Значения по умолчанию в примерах: 10 MiB, PDF/JPEG/PNG, PUT TTL 10 минут,
GET TTL 5 минут. TTL задаётся в целых секундах от 1 секунды до 7 дней.

Обязательные секреты передаются через окружение; в YAML они запрещены:

```text
POSTGRES_PASSWORD
S3_MEDIA_ACCESS_KEY
S3_MEDIA_SECRET_KEY
```

Внутренний `s3.endpoint` в Compose — `http://seaweedfs:8333`.
`s3.public_endpoint` — `http://localhost:8333`, доступный локальному клиенту.
Ссылки сразу подписываются для клиентского адреса. Если S3-порт или адрес
переопределён, обновите `public_endpoint`. При смене bucket согласуйте
конфигурацию Media, init-джобу и IAM-политику в `compose/seaweedfs/s3.json`.

## Запуск

Из корня репозитория:

```sh
docker compose up --build media
```

Compose поднимает PostgreSQL, SeaweedFS, Migrator и `seaweedfs-init`.
Media ждёт успешного завершения обеих джоб. gRPC доступен только локально:
`localhost:8085` (переопределение порта — `MEDIA_GRPC_PORT`). Внутри Compose
адрес сервиса — `media:8085`. Логи: `docker compose logs media`.

Для прямого запуска сначала подготовьте зависимости и миграции:

```sh
docker compose up -d postgres seaweedfs seaweedfs-init
docker compose run --rm --build migrator
```

Дождитесь успешного завершения `seaweedfs-init`. Не запускайте параллельно
второй Migrator. Затем в PowerShell:

```powershell
$env:POSTGRES_PASSWORD = "matchlab_local_only"
$env:S3_MEDIA_ACCESS_KEY = "matchlab_media_local"
$env:S3_MEDIA_SECRET_KEY = "matchlab_media_local_secret"
go run ./cmd/media -config cmd/media/config.example.yaml
```

Значения предназначены только для локального Compose с настройками по умолчанию.
Путь к YAML по умолчанию — `/etc/app/config.yaml`, переопределение — `-config`.
Сборка образа отдельно:

```sh
docker build -f cmd/media/Dockerfile -t media:local .
```

## gRPC API

Сервис доверяет `user_id` внутреннего вызывающего сервиса; он не проверяет
Bearer-токен. В приложении Gateway должен получать этот ID из проверенного
сеанса. Media не следует публиковать наружу. HTTP API подключён в Gateway:
см. [описание и примеры](../../api/http/README.md).

Все методы принадлежат `matchlab.media.v1.MediaService`:

| Метод | Назначение |
| --- | --- |
| `CreateUpload` | Создать `pending`, вернуть ID и подписанный PUT-запрос |
| `CompleteUpload` | Подтвердить объект через HEAD и сохранить `ready` |
| `GetFile` | Получить метаданные доступного файла без ссылки на содержимое |
| `CreateDownloadURL` | Выдать временный GET URL для `ready` |

Ошибки: `InvalidArgument` для неверных параметров, `NotFound` для отсутствующего
или чужого файла, `AlreadyExists` для дубликата, `FailedPrecondition` для
неподходящего состояния, отсутствующего объекта или неверных метаданных,
`Unavailable` для недоступного S3. Неожиданные ошибки возвращаются как `Internal`
без внутренних подробностей. Отмена и deadline сохраняют соответствующие коды.

`CreateUploadRequest.is_public` задаёт публичность при создании: если поле
не передано, файл приватный. `File.is_public` возвращается во всех ответах
с метаданными. Для публичного файла `GetFile` и `CreateDownloadURL` принимают
любой корректный `user_id` или пустое значение для гостя. Для приватного —
только ID владельца, иначе `NotFound`. `CreateUpload` и `CompleteUpload`
требуют ID владельца независимо от публичности. Изменение публичности после
создания пока не реализовано. Bucket остаётся закрытым: даже публичные файлы
скачиваются через временные подписанные URL.

Пример вызова через установленный `grpcurl`, из корня репозитория в PowerShell:

```powershell
$request = @{
    user_id = "00000000-0000-4000-8000-000000000001"
    original_name = "report.pdf"
    content_type = "application/pdf"
    size_bytes = "5"
    is_public = $false
} | ConvertTo-Json -Compress

$request | grpcurl -plaintext -import-path api/proto `
    -proto media/v1/media.proto -d '@' localhost:8085 `
    matchlab.media.v1.MediaService/CreateUpload
```

Выберите реальный размер файла. По полученной ссылке выполните PUT с байтами
файла и заголовками ответа. Затем вызовите `CompleteUpload` с `user_id` и
выданным `file_id`. Те же поля используются для `GetFile` и `CreateDownloadURL`.
Reflection не включён: `grpcurl` читает явный `.proto`.

Для публичного файла задайте `is_public = $true`. Гость может получить ссылку
запросом `CreateDownloadURL` только с `file_id`, без `user_id`.

## Генерация gRPC-кода

После изменения `api/proto/media/v1/media.proto` пересоздайте оба Go-файла.
Нужен установленный `protoc` и Go-плагины в PATH. Версии плагинов текущего
сгенерированного кода:

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
protoc --proto_path=. --go_out=. --go_opt=module=github.com/HSE-Scientists-Team/matchlab-backend --go-grpc_out=. --go-grpc_opt=module=github.com/HSE-Scientists-Team/matchlab-backend api/proto/media/v1/media.proto
```

Команды выполняются из корня репозитория. По умолчанию Go-плагины
устанавливаются в `go/bin` внутри домашнего каталога; добавьте его в PATH.
В текущих файлах использован protoc 34.1. Сгенерированный код находится
в `internal/gen/media/v1`; его не редактируют вручную.

## Проверка

Проверка пакетов: `go test ./internal/media/...`.
Интеграционная проверка PostgreSQL и SeaweedFS с Docker:

```sh
go test -tags=integration ./internal/media/repository ./internal/media/storage/s3
```

Полная проверка Docker-образа и gRPC-сценария:

```sh
go test -tags=integration -v ./cmd/media
```

Этот тест собирает Dockerfile Media и запускает его в отдельной сети с
PostgreSQL и SeaweedFS. Проверяет healthcheck, создание и чтение `pending`,
PUT, подтверждение и повтор, приватный доступ владельца, публичное чтение
другим пользователем и гостем, запрет подтверждения чужой загрузки и GET.

Тесты создают отдельные контейнеры и не используют данные из локального Compose.
S3-тест проверяет приватность bucket, доступ ключами Media, подписанный PUT/GET,
отклонение неверного размера/типа и запрет перезаписи объекта.

Если на Windows Testcontainers неправильно определяет сокет Docker Desktop,
задайте настройку пользователя в PowerShell и перезапустите терминал/IDE:

```powershell
[Environment]::SetEnvironmentVariable(
    "TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", "/var/run/docker.sock", "User"
)
```

## Публичный HTTP API

Gateway вызывает эти четыре RPC через `/api/v1/media/files`. Создание и
подтверждение требуют проверенного сеанса и почты. Чтение публичного файла
доступно гостю. Данные передаются напрямую через подписанные S3 URL.
