# Support

Support создаёт жалобы через внутренний gRPC `matchlab.support.v1.SupportService/CreateComplaint`.
Контракт: `api/proto/support/v1/support.proto`, сгенерированный Go-код: `internal/gen/support/v1`.
Данными таблиц `support.support_ticket` и `support.support_ticket_attachment` владеет Support.

## Создание жалобы

Запрос содержит `user_id`, `subject`, `description`, ровно один из `target_user_id` / `target_project_id`
и необязательный список `file_ids`. Автор передаётся Gateway из проверенного сеанса с подтверждённой почтой.
Все ID должны быть UUID, отличными от нулевого; цели проверяются по формату, без проверки существования:
User пока не предоставляет соответствующий RPC, сервис проектов ещё не реализован.

После удаления пробелов по краям тема должна содержать 1–255 символов, описание — 1–10000.
Допускается 0–20 вложений без повторов. Фронт заранее загружает и подтверждает файлы через Media.
Support вызывает Media.GetFile с ID автора и проверяет владельца и статус `ready`, включая публичные файлы.
Если любое вложение не прошло проверку, жалоба не сохраняется.
Жалоба и вложения создаются одной транзакцией PostgreSQL, статус всегда `open`.
Ответ содержит `id`, `status` и `created_at_unix`.

Статусы БД: `open`, `resolved`, `declined`. При создании назначенный сотрудник и `resolved_at` остаются NULL.
Межсервисных FK к Users/Media/проектам нет. Внутренний FK вложений использует `NO ACTION`.
Проверка Media и запись Support не являются распределённой транзакцией: удаление файла после проверки
не блокируется. Политика удержания доказательств и доступ сотрудников к приватным файлам нужны
при реализации рассмотрения жалоб. Создание не идемпотентно; автоматических повторов нет.

## Ошибки

- `InvalidArgument`: некорректные поля, отсутствующая/двойная цель, дубли вложений.
- `NotFound`: файл отсутствует или принадлежит другому пользователю.
- `FailedPrecondition`: файл не в состоянии `ready`.
- `Unavailable`: Media недоступен.
- `Internal`: ошибка записи, подробности БД клиенту не выдаются.
- `Canceled` / `DeadlineExceeded`: отмена / истечение срока выполнения.

Каждый RPC ограничен 10 секундами, каждый вызов Media — 3 секундами; короткий deadline клиента сохраняется.

## Запуск

Весь проект: `docker compose up --build`. В Compose Support доступен только внутри сети как `support:8086`.
Для прямого запуска предварительно запустите PostgreSQL и Media, задайте `POSTGRES_PASSWORD`:

```powershell
$env:POSTGRES_PASSWORD = 'matchlab_local_only'
go run ./cmd/support -config cmd/support/config.example.yaml
```

Несекретные настройки читаются из YAML, по умолчанию `/etc/app/config.yaml`, пароль БД — только из окружения.
До открытия gRPC сервис применяет весь общий набор миграций и проверяет подключения к PostgreSQL и Media.
Миграция `00004` создаёт таблицы Support; её Down удаляет все жалобы и вложения.
Зарегистрированы healthcheck для пустого имени и `matchlab.support.v1.SupportService`.
При SIGTERM/SIGINT сервис объявляет NOT_SERVING и завершает активные запросы в течение 10 секунд.

## Генерация и проверка

Используйте protoc 34.1, protoc-gen-go v1.36.12, protoc-gen-go-grpc v1.5.1:

```sh
protoc --proto_path=. --go_out=. --go_opt=module=github.com/HSE-Scientists-Team/matchlab-backend --go-grpc_out=. --go-grpc_opt=module=github.com/HSE-Scientists-Team/matchlab-backend api/proto/support/v1/support.proto
go test ./...
go test -tags=integration ./internal/support/repository ./pkg/migrations
```
