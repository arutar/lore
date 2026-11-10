# Текущее состояние Lore Platform

Обновлено для commit `9081018d` (`Add self-hosted Lore platform and Windows clients`), отправленного в `origin/main`.

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
- Caddy HTTPS проверен локально на `lore.internal`; публичный ACME ещё не проверен.
- Linux CLI installer проверен в чистом Debian container.
- Windows companion на локальном React + WebView2 для всех восьми операций; WPF используется только как оконный host. Старый XAML UI удалён.
- JSON request files вместо named pipe, all-users/per-user installers, проверка WebView2 Runtime и выбор локального bootstrapper, читаемые ошибки операций.
- Полный набор из 28 SVG-иконок, PNG/ICO exports, воспроизводимые генераторы и автономный просмотрщик: `platform/windows/shared/svg/preview.html`.
- Per-machine MSI на WiX 7.0.1: нативная регистрация shell/COM, machine PATH, стабильный UpgradeCode. GitHub Actions собирает MSI/ZIP/SHA256SUMS, тестирует install/uninstall и публикует по `platform-v*` через `platform-release` environment. Workflow прошёл actionlint.
- Upstream contribution для `http+insecure`: открыт EpicGames/lore#278. До ответа maintainers PR не создаётся; затем patch, release note, DCO и Rust проверки на свежем `upstream/main`.

## Важные ограничения

- Release и ASAN smoke-тесты Explorer проходят, включая иконки submenu и собственные overlay resources. Для окончательной приёмки нужна повторная установка и проверка в Explorer. Если DLL отключена, сначала проверить её состояние, не регистрировать повторно автоматически.
- MSI проведён только administrative extract; полный install/uninstall smoke подготовлен, но GitHub runner ещё не выполнял workflow. WiX Burn setup и code signing пока не реализованы.
- Публичный HTTPS/DNS, реальный AD и SMTP delivery ещё не проверены.
- Registration toggle в WebUI пока не синхронизирован с Keycloak.
- Нет tree/history/download в WebUI.
- Нет полного backup/restore и production release hardening.
- Dev-пароли в исходных шаблонах не предназначены для production; setup создаёт случайные значения.

## Следующие этапы

1. AD/LDAP, стабильные групповые идентификаторы, внешняя регистрация и SMTP.
2. Содержимое репозиториев в WebUI: tree, download, history, locks.
3. Backup/restore PostgreSQL, Lore storage и signing key.
4. Повторная Explorer acceptance, подпись, uninstall и release review.

## Полезные команды

```powershell
# Запуск хоста
platform/deploy/setup.ps1

# Обновление Windows-клиента
platform/clients/Install-Windows.ps1 -AllUsers

# Проверки
cd platform/control-plane; go test ./...
cd platform/web; npm run build
```

Подробная архитектура и диаграммы: `platform/docs/architecture.md`.
