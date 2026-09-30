# Сервис Gateway

Gateway — публичная точка входа по HTTP. Он отвечает за маршруты, идентификаторы запросов, промежуточные обработчики и преобразование ошибок gRPC в HTTP. Gateway не обращается к PostgreSQL или Redis напрямую. Auth хранит и проверяет сеансы, User хранит учётные записи и обрабатывает регистрацию и вход по паролю. Gateway вызывает оба сервиса по gRPC.

## Конфигурация и локальный запуск

Путь к конфигурации по умолчанию — `/etc/app/config.yaml`; изменить его можно флагом `-config`. В `config.example.yaml` указаны хосты и порты HTTP и внутренних gRPC-сервисов.

Запустите весь проект из корня репозитория (PostgreSQL, Redis, Auth, User, Mail и Gateway):

```sh
docker compose up --build
```

Gateway доступен по адресу `http://localhost:8080`. Для остановки нажмите `Ctrl+C`; команда `docker compose down` остановит и удалит контейнеры. Данные PostgreSQL сохранятся в именованном томе. Compose использует файлы `config.compose.yaml` с DNS-именами сервисов Docker; конфигурации для `go run` используют `localhost`.

Интерактивное описание реализованного HTTP API и примеры запросов доступны в [Scalar](../../api/http/README.md) по адресу `http://localhost:8084` после запуска Compose.
Для интерактивных запросов из Scalar локальная конфигурация разрешает CORS с адреса документации; в `config.example.yaml` список разрешённых источников пуст.

Чтобы запускать Auth, User и Gateway напрямую, сначала поднимите зависимости и Mail через Compose:

```sh
docker compose up -d postgres redis mailpit mail
```

Затем запустите каждый сервис в отдельном терминале:

```sh
export POSTGRES_PASSWORD=matchlab_local_only
export REDIS_PASSWORD=matchlab_redis_local
go run ./cmd/auth -config cmd/auth/config.example.yaml
```

```sh
export POSTGRES_PASSWORD=matchlab_local_only
export REDIS_PASSWORD=matchlab_redis_local
go run ./cmd/user -config cmd/user/config.example.yaml
```

```sh
go run ./cmd/gateway -config cmd/gateway/config.example.yaml
```

Учётные данные Compose предназначены только для локальной разработки. Данные PostgreSQL хранятся в именованном томе. У Redis тома нет, поэтому при его перезапуске сеансы удаляются.

## HTTP API

- `GET /health`: проверка работоспособности процесса.
- `POST /api/v1/auth/register`: JSON `{ "login": "...", "password": "..." }`; возвращает `201` и новый `user_id`.
- `POST /api/v1/auth/login`: тот же формат JSON; возвращает `user_id` и `access_token`.
- `GET /api/v1/users/me/email`: требует токен доступа; возвращает состояние адреса (`not_set`, `pending` или `verified`) и текущий или ожидающий подтверждения адрес.
- `POST /api/v1/users/me/email`: требует токен доступа и JSON `{ "email": "..." }`; дожидается принятия письма SMTP-сервером и возвращает `204 No Content`; при ошибке SMTP возвращает ошибку.
- Для адреса вне активного списка организаций этот метод возвращает `403` и не отправляет письмо. Тот же ответ возможен при подтверждении ранее выданного токена, если организацию отключили.
- `POST /api/v1/auth/email/confirm`: JSON `{ "token": "..." }`; подтверждает адрес, связанный с одноразовым токеном, и возвращает `204`.
- `GET /api/v1/sessions/current`: требует `Authorization: Bearer <token>` и возвращает `user_id` сеанса.
- `DELETE /api/v1/sessions/current`: отзывает токен доступа и возвращает `204`.

Регистрация и вход направляются в User, проверка и отзыв сеанса — в Auth. Маршруты находятся под `/api/v1`: новые HTTP API можно добавлять в отдельные группы без зависимости Gateway от хранилищ сервисов. Все ответы содержат `X-Request-ID`; пароли и токены не записываются в журнал. Для просмотра тестовых писем Compose запускает Mailpit по адресу `http://localhost:8025`.

Проверка: `go test ./...`. Сборка образа: `docker build -f cmd/gateway/Dockerfile -t gateway:local .`.
