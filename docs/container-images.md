# Сборка образов в GitHub

Workflow [build-images.yml](../.github/workflows/build-images.yml) запускается вручную через **Actions → Собрать и опубликовать образы → Run workflow**. Выберите ветку и `service`: `all` либо один из `gateway`, `auth`, `user`, `mail`. Кнопка появится после добавления workflow в default branch репозитория.

Перед публикацией выполняются обычные и интеграционные тесты. При `all` четыре образа собираются параллельно. Платформа — `linux/amd64`, подходящая для ВМ на x86-хосте Proxmox. Для ARM-кластера настройте отдельную платформу или multi-platform build.

Образы публикуются в GHCR:

```text
ghcr.io/hse-scientists-team/matchlab-backend/gateway:sha-<полный SHA коммита>
ghcr.io/hse-scientists-team/matchlab-backend/auth:sha-<полный SHA коммита>
ghcr.io/hse-scientists-team/matchlab-backend/user:sha-<полный SHA коммита>
ghcr.io/hse-scientists-team/matchlab-backend/mail:sha-<полный SHA коммита>
```

Используется встроенный `GITHUB_TOKEN` с `packages: write`; отдельный пароль для CI не нужен. Политика организации должна разрешать Actions и создание Packages. Видимость новых пакетов проверьте отдельно: для приватных образов кластеру понадобится read-only GHCR credential. Workflow не делает образы публичными автоматически. В summary каждой сборки выводятся тег и digest; digest позволяет закрепить точный результат сборки, тогда как повторный запуск на том же SHA может перезаписать тег.

Для полноценного релиза текущая инфраструктурная база ожидает четыре образа одного коммита: выбирайте `all`. Сборка одного сервиса полезна для проверки или повторной публикации недостающего образа того же коммита. Она сама не меняет версии в кластере.

После успешной сборки обновите `RELEASE` во Flux Kustomization `matchlab-backend` в `matchlab-infra/clusters/homelab/releases.yaml` и внесите изменение в Git. Flux обновит backend; каждый процесс применит общий SQL-набор под общей PostgreSQL-блокировкой до открытия API. Это отдельное действие выпуска: сборка кода не даёт CI доступ к кластеру и не требует kubeconfig.

Инфраструктура и инструкции по установке: [matchlab-infra](https://github.com/HSE-Scientists-Team/matchlab-infra).
