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
ключи. Локальные значения по умолчанию:

```text
access key: matchlab_media_local
secret key: matchlab_media_local_secret
bucket:     matchlab-media
region:     us-east-1
```

Их можно переопределить переменными `S3_ACCESS_KEY`, `S3_SECRET_KEY`,
`S3_BUCKET` и `S3_REGION`. Значения по умолчанию предназначены только для
локальной разработки.

Init-джоба не удаляет bucket и файлы при повторном запуске. Остановить сервисы:

```sh
docker compose stop seaweedfs
```

Удаление тома `seaweedfs-data` безвозвратно удалит загруженные объекты.
