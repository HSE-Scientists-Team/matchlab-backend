# Локальное S3-хранилище SeaweedFS

SeaweedFS запускается в односерверном режиме `weed mini` только для локальной
разработки. Данные сохраняются в именованном томе `seaweedfs-data`. Одноразовая
джоба `seaweedfs-init` ждёт готовности S3 API, идемпотентно создаёт приватный
bucket `matchlab-media` и применяет CORS из `cors.json`.

Запуск только объектного хранилища и инициализации:

```sh
docker compose up -d seaweedfs seaweedfs-init
```

Проверка состояния и логов инициализации:

```sh
docker compose ps
docker compose logs seaweedfs-init
```

Локальные адреса:

- Admin UI: `http://localhost:12646`;
- S3 API: `http://localhost:8333`;
- Filer UI: `http://localhost:8888`;
- Master UI: `http://localhost:9333`.

Admin, Filer и Master UI не имеют прикладной авторизации и поэтому привязаны
только к `127.0.0.1`. Не публикуйте эти порты во внешней сети. S3 API требует
ключи. Административные локальные значения по умолчанию:

```text
access key: matchlab_admin_local
secret key: matchlab_admin_local_secret
bucket:     matchlab-media
region:     us-east-1
```

Для Media-сервиса объявлена отдельная статическая IAM-учётка с
правами только на bucket `matchlab-media`:

```text
access key: matchlab_media_local
secret key: matchlab_media_local_secret
policy:     matchlab-media-rw
```

Политика разрешает CRUD объектов и multipart-upload, но не разрешает создавать
или удалять bucket, управлять IAM и обращаться к другим bucket. Учётка и
политика декларативно описаны в `s3.json`; секреты подставляются из переменных
окружения и не хранятся в этом файле.

Значения можно переопределить переменными `S3_ACCESS_KEY`, `S3_SECRET_KEY`,
`S3_MEDIA_ACCESS_KEY`, `S3_MEDIA_SECRET_KEY`, `S3_BUCKET` и `S3_REGION`.
Значения по умолчанию предназначены только для локальной разработки.

Init-джоба не удаляет bucket и файлы при повторном запуске. Остановить сервисы:

```sh
docker compose stop seaweedfs
```

Удаление тома `seaweedfs-data` безвозвратно удалит загруженные объекты.

## Работа через Media

Gateway и Media используют S3 для загрузки и скачивания по подписанным URL:
см. [пример HTTP API](../../api/http/README.md). Media сохраняет метаданные
в PostgreSQL и формирует ключ `users/{owner_user_id}/{file_id}` в bucket
`matchlab-media`. Оригинальное имя не входит в ключ; оно хранится отдельно
и используется как имя скачиваемого файла. Объект создаётся при PUT байтов,
а не при создании записи pending.

`is_public` хранится в PostgreSQL. Публичный файл разрешено читать гостю,
но сам bucket остаётся закрытым: доступ к объекту даёт подписанная ссылка.
Init-джоба не меняет публичность записей Media. Она также не обновляет его
настройки: при изменении `S3_BUCKET` или `S3_REGION` согласуйте YAML Media
и IAM-политику. `S3_PORT` должен совпадать с клиентским `s3.public_endpoint`.
Не меняйте адрес уже подписанного URL — подпись зависит от host.
