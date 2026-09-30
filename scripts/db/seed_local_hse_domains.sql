-- Явные тестовые данные для локального PostgreSQL. Скрипт не выполняется
-- автоматически при запуске приложения или мигратора
INSERT INTO users.trusted_email_domain (domain, organization_name, is_active)
VALUES
    ('hse.ru', 'НИУ ВШЭ', true),
    ('*.hse.ru', 'НИУ ВШЭ', true)
ON CONFLICT (domain) DO NOTHING;
