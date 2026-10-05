# Сервис Auth

Auth хранит краткосрочные сеансы входа в Redis. При запуске сервис временно подключается к PostgreSQL и применяет общий SQL-набор под advisory-блокировкой; после этого PostgreSQL в обработке сеансов не участвует. Учётные записи и хеши паролей принадлежат сервису User. Auth предоставляет внутренний gRPC API: Gateway проверяет и отзывает сеансы, а User создаёт сеанс после успешного входа.

## Конфигурация и локальный запуск

По умолчанию сервис читает YAML из `/etc/app/config.yaml`. Путь можно изменить флагом `-config`. Хост, порт и номер базы Redis задаются в YAML; пароль передаётся через `REDIS_PASSWORD`.

Чтобы запустить весь проект, выполните `docker compose up --build` в корне репозитория. Для отдельного запуска Auth сначала запустите Redis:

```sh
docker compose up -d postgres redis
export POSTGRES_PASSWORD=matchlab_local_only
export REDIS_PASSWORD=matchlab_redis_local
go run ./cmd/auth -config cmd/auth/config.example.yaml
```

Пароль в Compose предназначен только для разработки. У Redis нет постоянного тома: после перезапуска или пересоздания контейнера сеансы удаляются, и пользователям нужно войти повторно. Сохранение данных в рабочем окружении настраивается в инфраструктурном репозитории.

## gRPC API

- `CreateSession(user_id)` создаёт криптографически случайный токен доступа.
- `ValidateSession(token)` возвращает идентификатор пользователя или ошибку `Unauthenticated`.
- `RevokeSession(token)` удаляет сеанс; отзыв отсутствующего корректного токена идемпотентен.

В качестве ключа Redis используется только SHA-256-хеш токена. Токены и пароль Redis не записываются в журнал. Срок действия сеанса — 24 часа. Проверить пакеты сервиса можно командой `go test ./internal/auth/...`. Интеграционный тест использует Testcontainers и настоящий образ Redis: `go test -tags=integration ./internal/auth/repository/redis`. Образ сервиса собирается из корня репозитория командой `docker build -f cmd/auth/Dockerfile -t auth:local .`.

При запуске обязательны настройки `postgres` и переменная `POSTGRES_PASSWORD` для применения всего SQL-набора. `postgres.migration_timeout` по умолчанию равен `5m`; ошибочная или более новая схема блокирует запуск API. Секрет PostgreSQL не хранится в YAML. Подробнее — [миграции](../../pkg/migrations/README.md).
