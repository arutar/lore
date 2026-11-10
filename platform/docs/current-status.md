# Текущее состояние Lore Platform

Обновлено после commit `a5fd239f`; описанные ниже AD/LDAP и registration-изменения пока находятся в рабочей ветке.

## Цель

Self-hosted Lore с React WebUI, Go control plane, PostgreSQL, Keycloak, AD/LDAP и внешними email-пользователями. Роли admin/user/guest, additive grants `none < read < write < owner`, приватные репозитории скрыты без grant. Windows клиент включает CLI, companion и Explorer integration; Linux использует CLI.

## Реализовано и проверено

- WebUI: каталог, пользователи, репозитории, grants, audit, CSV/JSON import/export и глобальные темы из SoftwareVoronezh.
- Lore gRPC `UserService` и `RepositoryRegistryService`.
- Настоящий `RepositoryCreate` из WebUI через service account.
- ACL-aware OIDC token exchange с repository-scoped JWT.
- Реальный сценарий `Clone → Stage → Commit → Sync` через официальный `LoreVcs` SDK.
- Compose с PostgreSQL, Keycloak, Lore, Caddy и healthchecks.
- Host bind storage: `platform/deploy/data/`; `LORE_DATA_ROOT` меняет расположение.
- Setup: `platform/deploy/setup.sh` и `setup.ps1`; случайные секреты, один домен по умолчанию, отдельный auth-домен опционально.
- Registration toggle синхронизирован с Keycloak Admin API; service account автоматически получает `manage-realm` при setup.
- Реальный AD вход проверен через полный OIDC flow. Control plane получил 17 nested groups как стабильные `objectGUID`; LDAP provider использует read-only mode и доверенные AD email.
- Реальный SSL SMTP на порту 465 и внешняя регистрация проверены через форму Keycloak, включая повторную отправку verification email. Тестовые пользователи удалены, регистрация восстановлена в выключенное состояние.
- WebUI показывает tree, history и locks через штатные Lore gRPC services. Download проверяет путь в ревизии, читает полный CAS address и собирает fragmented/LZ4/Zstd content.
- На реальном репозитории проверены OIDC permissions, tree/history/locks и побайтовое скачивание README и fragmented 1 МиБ файла. Go unit tests и WebUI build проходят.
- Backup/restore включает базы `lore` и `keycloak`, CAS storage и signing key. Windows и shell архивы взаимно совместимы; кросс-restore, SHA-256 и healthchecks после запуска проверены.
- LDAP/SMTP JSON generation и escaping проверены для обеих setup-версий, включая свежий setup в Windows PowerShell 5.
- Caddy HTTPS проверен локально на `lore.internal`; публичный ACME ещё не проверен.
- Linux CLI installer проверен в чистом Debian container.
- Windows companion на локальном React + WebView2 для всех восьми операций; WPF используется только как оконный host. Старый XAML UI удалён.
- JSON request files вместо named pipe, all-users/per-user installers, проверка WebView2 Runtime и выбор локального bootstrapper, читаемые ошибки операций.
- Полный набор из 28 SVG-иконок, PNG/ICO exports, воспроизводимые генераторы и автономный просмотрщик: `platform/windows/shared/svg/preview.html`.
- Per-machine MSI на WiX 7.0.1: нативная регистрация shell/COM, machine PATH, стабильный UpgradeCode. GitHub Actions собрал MSI/ZIP/SHA256SUMS и успешно выполнил install/uninstall smoke.
- Upstream contribution для `http+insecure`: открыт EpicGames/lore#278. До ответа maintainers PR не создаётся; затем patch, release note, DCO и Rust проверки на свежем `upstream/main`.

## Важные ограничения

- Release и ASAN smoke-тесты Explorer проходят, включая иконки submenu и собственные overlay resources. Для окончательной приёмки нужна повторная установка и проверка в Explorer. Если DLL отключена, сначала проверить её состояние, не регистрировать повторно автоматически.
- WiX Burn setup и code signing пока не реализованы.
- Публичный HTTPS/DNS с ACME ещё не проверены.
- Production release hardening ещё не завершён.
- Dev-пароли в исходных шаблонах не предназначены для production; setup создаёт случайные значения.

## Следующие этапы

1. Повторная Explorer acceptance, подпись, uninstall и release review.

## Полезные команды

```powershell
# Запуск хоста
platform/deploy/setup.ps1

# Backup и restore
platform/deploy/backup.ps1
platform/deploy/restore.ps1 -Backup platform/deploy/backups/BACKUP_NAME

# Обновление Windows-клиента
platform/clients/Install-Windows.ps1 -AllUsers

# Проверки
cd platform/control-plane; go test ./...
cd platform/web; npm run build
```

Подробная архитектура и диаграммы: `platform/docs/architecture.md`.
