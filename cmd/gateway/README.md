# Сервис Gateway

Gateway — публичная точка входа по HTTP. Он отвечает за маршруты, идентификаторы запросов, промежуточные обработчики и преобразование ошибок gRPC в HTTP. Перед открытием HTTP Gateway подключается к PostgreSQL только для общего запуска миграций. HTTP-запросы обслуживаются через внутренние сервисы; прямого доступа к данным PostgreSQL и Redis нет. Auth хранит и проверяет сеансы, User хранит учётные записи и обрабатывает регистрацию и вход по паролю. Gateway вызывает оба сервиса по gRPC.

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
export POSTGRES_PASSWORD=matchlab_local_only
go run ./cmd/gateway -config cmd/gateway/config.example.yaml
```

Учётные данные Compose предназначены только для локальной разработки. Данные PostgreSQL хранятся в именованном томе. У Redis тома нет, поэтому при его перезапуске сеансы удаляются.

## HTTP API

- `POST /api/v1/auth/register`: JSON `{ "email": "student@hse.ru", "password": "..." }`; возвращает `201` и `{ "status": "pending" }` после принятия письма SMTP. Аккаунта и сеанса ещё нет. Повтор с тем же email и выбранным паролем заменяет заявку и отправляет новую ссылку; старые ссылки недействительны. Для существующего аккаунта — `409`.
- `POST /api/v1/auth/login`: тот же JSON; возвращает `user_id` и `access_token` только после подтверждения почты. До подтверждения аккаунта нет, поэтому вход возвращает `401`.
- `POST /api/v1/auth/email/confirm`: публичный метод, JSON `{ "token": "..." }`; создаёт аккаунт из актуальной заявки, успех — `204`, истёкший токен — `410`, использованный — `404`.
- `GET /api/v1/users/me/email`: требует сеанс и подтверждённую почту; возвращает состояние адреса. Смена почты не поддерживается.
- `GET /api/v1/sessions/current`: требует Bearer-токен и подтверждённую почту, возвращает `user_id`.
- `DELETE /api/v1/sessions/current`: отзывает Bearer-токен, возвращает `204`; отзыв доступен и для старого неподтверждённого сеанса.
- `GET /health`: проверка процесса.

Фронтенд после регистрации показывает «Перейдите по ссылке в письме и подтвердите регистрацию». Mail добавляет токен в URL из `smtp.verification_url` (локально `http://localhost:5173/verify-email`). На этой странице фронтенд читает `token` из query string, вызывает API подтверждения и после `204` показывает «Адрес подтверждён» с переходом ко входу. Страница фронтенда находится в отдельном приложении. В этом репозитории реализован API для неё.

Регистрация и подтверждение не требуют сеанса. `AuthenticatedSubrouter` проверяет сеанс и email через User, включая сеансы, выданные до обновления. Публичные группы маршрутов сохраняют анонимный доступ. Все ответы содержат `X-Request-ID`; пароли и токены не записываются в журнал. Тестовые письма доступны в Mailpit: `http://localhost:8025`.

Проверка: `go test ./...`. Сборка образа: `docker build -f cmd/gateway/Dockerfile -t gateway:local .`.

При запуске обязательны настройки `postgres` и переменная `POSTGRES_PASSWORD` для применения всего SQL-набора. `postgres.migration_timeout` по умолчанию равен `5m`; ошибочная или более новая схема блокирует запуск API. Секрет PostgreSQL не хранится в YAML. Подробнее — [миграции](../../pkg/migrations/README.md).
