# HTTP API Gateway

[`openapi.yaml`](openapi.yaml) описывает текущие публичные маршруты Gateway, примеры JSON-запросов и ответов, ошибки и Bearer-авторизацию. Scalar показывает эту спецификацию как интерактивную документацию. Это описание фактически реализованного API, а не будущих сценариев продукта.

Для локального запуска выполните из корня репозитория:

```sh
docker compose up --build
```

Откройте `http://localhost:8084`. Scalar по умолчанию направляет интерактивные запросы напрямую в Gateway по адресу `http://localhost:8080`. Для браузерного доступа Gateway разрешает CORS только с `http://localhost:8084` и `http://127.0.0.1:8084` в локальной конфигурации. Порт документации привязан к `127.0.0.1`; в продакшене его публикацию и контроль доступа нужно настраивать отдельно. Если меняете `API_DOCS_PORT`, обновите разрешённые адреса в `cmd/gateway/config.compose.yaml`.

Для проверки сценария сначала добавьте локальные правила HSE командой из [README User](../../cmd/user/README.md), затем зарегистрируйтесь с `email: student@hse.ru` и паролем. Письмо сразу появится в Mailpit (`http://localhost:8025`). Передайте токен из ссылки в `POST /api/v1/auth/email/confirm`, затем войдите по email и паролю и укажите `access_token` в Bearer Auth. Регистрация возвращает только `status: pending`, без `user_id`: аккаунт создаётся при подтверждении. До этого вход возвращает `401`. Для нового письма повторите регистрацию с тем же email и выбранным паролем; новая заявка заменяет пароль и аннулирует старую ссылку. Примеры содержат учебные значения.

Исходники интерфейса Scalar не загружаются с CDN при открытии страницы: при сборке Docker-образа берётся зафиксированная версия браузерного бандла, проверяется её контрольная сумма и файл обслуживается локально. При изменении HTTP-маршрута, схемы или кода ответа обновляйте `openapi.yaml` вместе с Gateway.

## Файлы

После изменения спецификации пересоберите документацию:
`docker compose up -d --build --no-deps api-docs`. После изменения HTTP-кода
пересоберите также Gateway: `docker compose up -d --build gateway media api-docs`.
Откройте именно `http://localhost:8084` и обновите страницу через Ctrl+F5.
Текущий файл контейнера доступен по `http://localhost:8084/openapi.yaml`.

| Вызов | Результат |
| --- | --- |
| `POST /api/v1/media/files` | `201`: запись pending, upload_url, method, headers, expires_at_unix |
| `POST /api/v1/media/files/{id}/complete` | `200`: метаданные подтверждённого файла |
| `GET /api/v1/media/files/{id}` | `200`: метаданные в объекте file |
| `GET /api/v1/media/files/{id}/download-url` | `200`: download_url и expires_at_unix |

Создание и подтверждение требуют Bearer-токена и подтверждённой почты.
Gateway определяет владельца из сеанса. Публичный файл (`is_public: true`)
можно читать и скачивать без Authorization; приватный доступен владельцу.
Если токен передан, он должен быть действительным. Чужой или отсутствующий
приватный файл возвращает `404`. Подтверждать публичный файл может только
владелец. Изменение публичности, удаление и список файлов пока не реализованы.

Полный пример в PowerShell после входа (положите настоящий `avatar.png`
в текущий каталог и замените значение токена):

```powershell
$base = 'http://localhost:8080/api/v1/media/files'
$authHeaders = @{ Authorization = 'Bearer YOUR_ACCESS_TOKEN' }
$filePath = (Resolve-Path './avatar.png').Path
$body = @{
    original_name = 'avatar.png'
    content_type = 'image/png'
    size_bytes = (Get-Item -LiteralPath $filePath).Length
    is_public = $true
} | ConvertTo-Json -Compress

$upload = Invoke-RestMethod -Method Post -Uri $base -Headers $authHeaders `
    -ContentType 'application/json' -Body $body
$id = $upload.file.id

# Передаём байты файла в S3; токен Gateway здесь не нужен.
$putHeaders = @{}
$upload.headers.PSObject.Properties | ForEach-Object {
    if ($_.Name -notin @('Content-Length', 'Content-Type')) {
        $putHeaders[$_.Name] = $_.Value
    }
}
Invoke-WebRequest -Method Put -Uri $upload.upload_url -InFile $filePath `
    -ContentType 'image/png' -Headers $putHeaders

Invoke-RestMethod -Method Post -Uri "$base/$id/complete" -Headers $authHeaders
Invoke-RestMethod -Method Get -Uri "$base/$id"
$download = Invoke-RestMethod -Method Get -Uri "$base/$id/download-url"
Invoke-WebRequest -Uri $download.download_url -OutFile './downloaded-avatar.png'
```

Content-Length вычисляется из файла автоматически. Для приватного файла
задайте `is_public = $false` и передайте `$authHeaders` также в два GET-запроса
Gateway. В S3 Bearer-токен не передаётся. Временная ссылка доступна любому
её получателю до истечения TTL; после истечения запросите новую.

`upload_url` предназначен для PUT, его открытие в браузере не загружает файл.
`download_url` предназначен для GET: его можно открыть в браузере, файл
скачается с оригинальным именем. Локально загрузочная ссылка действует
10 минут, ссылка на скачивание — 5 минут.

JSON может отображать `&` как `\u0026`. `Invoke-RestMethod` и `JSON.parse`
сами разбирают экранирование. Если копируете URL из сырого JSON вручную,
замените `\u0026` на `&`; остальные параметры и адрес подписанной ссылки
не изменяйте. Размер и Content-Type PUT должны совпадать с запросом создания,
а заголовок `If-None-Match: *` запрещает перезапись существующего объекта.

Ошибки: `400` — JSON или параметры, `401` — токен, `403` — почта/права,
`404` — файл недоступен, `409` — неподходящее состояние или объект,
`500` — внутренняя ошибка, `503` — зависимость недоступна, `504` — тайм-аут Media.
Запрос ссылок не кешируется. Gateway не принимает multipart и не проксирует байты.

Если `POST /files` возвращает текстовое `404 page not found`, проверьте,
что Gateway пересобран и запрос направлен на `localhost:8080`. Ошибки
обработчика Media возвращаются JSON-объектом `error`. Для старой локальной БД
без `is_public` добавьте колонку по [инструкции Migrator](../../cmd/migrator/README.md);
повторный запуск уже применённой миграции её не добавляет.
