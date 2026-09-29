-- Однократная очистка БД после перехода на синхронную отправку Mail.
-- Запускайте вручную только после решения судьбы старых писем в очереди:
-- удаление mail.email_outbox безвозвратно удаляет ожидающие задания.
BEGIN;
DROP TABLE IF EXISTS mail.email_outbox;
DROP TABLE IF EXISTS mail.goose_db_version;
DROP SCHEMA IF EXISTS mail;
COMMIT;
