# Текущее состояние Lore Platform

Обновлено после локальной HTTP-приёмки WebUI, AD/SMTP и native Lore; изменения пока находятся в рабочей ветке.

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
- Setup: `platform/deploy/setup.sh` и `setup.ps1`; случайные секреты, один домен по умолчанию, отдельный auth-домен опционально. Публичный URL, LDAP и SMTP сохраняются в `.env`, обычный `docker compose up` после restart не требует ручных переменных. Все сервисы имеют `restart: unless-stopped`.
- Registration toggle синхронизирован с Keycloak Admin API; service account автоматически получает `manage-realm` и `manage-users`. Без SMTP setup отключает verification/reset email, локальные учётки создаёт администратор.
- Реальный AD вход проверен через полный OIDC flow. Control plane получил 17 nested groups как стабильные `objectGUID`; LDAP provider использует read-only mode и доверенные AD email. Внешние email-пользователи не вызывают LDAP, режим без LDAP проверен.
- Локальные пользователи создаются из WebUI с email и временным паролем; disable/enable синхронизирован с Keycloak. Полный OIDC logout завершает обе сессии и требует пароль при следующем входе. Админка Keycloak и master realm закрыты на Caddy, отдельный порт Keycloak не публикуется.
- Реальный SSL SMTP на порту 465 и внешняя регистрация проверены через форму Keycloak; verification email успешно отправлен. Регистрация восстановлена в выключенное состояние.
- Native HTTP E2E: browser/device login, clone, запрет push при read-only, push после write grant и sync во втором рабочем каталоге. Исправлены read permission и `root_domains` в exchanged JWT.
- WebUI показывает tree, history и locks через штатные Lore gRPC services. History сопоставляет UUID авторов с логинами; используются официальные Lore favicon и знак без TM. Download проверяет путь в ревизии, читает полный CAS address и собирает fragmented/LZ4/Zstd content.
- На реальном репозитории проверены OIDC permissions, tree/history/locks и побайтовое скачивание README и fragmented 1 МиБ файла. Go unit tests и WebUI build проходят.
- Backup/restore включает базы `lore` и `keycloak`, CAS storage и signing key. Windows и shell архивы взаимно совместимы; кросс-restore, SHA-256 и healthchecks после запуска проверены.
- LDAP/SMTP JSON generation и escaping проверены для обеих setup-версий, включая свежий setup в Windows PowerShell 5.
- Caddy HTTPS проверен локально на `lore.internal`; публичный ACME ещё не проверен.
- Linux CLI installer проверен в чистом Debian container.
- Windows companion на локальном React + WebView2 для всех восьми операций; WPF используется только как оконный host. Старый XAML UI удалён.
- JSON request files вместо named pipe, all-users/per-user installers, проверка WebView2 Runtime и выбор локального bootstrapper, читаемые ошибки операций.
- Полный набор из 28 SVG-иконок, PNG/ICO exports, воспроизводимые генераторы и автономный просмотрщик: `platform/windows/shared/svg/preview.html`.
- Per-machine MSI на WiX 7.0.1: нативная регистрация shell/COM, machine PATH, стабильный UpgradeCode. GitHub Actions собрал MSI/ZIP/SHA256SUMS и успешно выполнил install/uninstall smoke.
- MSI versioning исправлен: единый `PlatformVersion` попадает в companion, shell и installer. Подтверждена замена старой unversioned DLL; итоговая установка в `C:\Program Files\Lore` побайтово совпадает с payload и проходит shell smoke. Companion tests: 21 passed, 4 live tests пропущены по назначению.
- Accounts поддерживает выбор и удаление сохранённых учёток. Add и вход из Retry автоматически назначают учётку серверу; операции получают её явный `userId`. Живой Clone приватного `megatest_repo` под admin проходит. Remove передаёт текущий access token в `/web/v1/native-logout`; control plane проверяет подпись и владельца, включая истёкшие JWT, и завершает его Keycloak-сессии через Admin API, затем выполняет Lore AuthLogout. Refresh и Sync перед удалением не нужны; учётка без токена тоже удаляется локально. Тихий выход без браузера подтверждён пользователем. Device flow и один экран Grant Access сохраняются.
- Bootstrap admin получает `default-roles-lore` с `offline_access`; скрытый приватный Clone показывает ошибку доступа и кнопку смены учётки.
- Explorer визуально принят: группа меню с сепараторами, прозрачные иконки и clean-overlay на корневой папке. CLSID MSI сверены с `Guids.h`; shell smoke проверяет separators, alpha и clean membership. Status агрегирует состояния в родительские папки, `.lore` скрывается, пустой commit message разрешён.
- Upstream contribution для `http+insecure`: открыт EpicGames/lore#278. До ответа maintainers PR не создаётся; затем patch, release note, DCO и Rust проверки на свежем `upstream/main`.

## Важные ограничения

- Одна старая renamed DLL остаётся занятой запущенным Total Commander до его перезапуска; рабочая MSI DLL установлена и используется новым Explorer.
- WiX Burn setup и code signing пока не реализованы.
- Публичный HTTPS/DNS с ACME ещё не проверены; compose-конфигурации direct Caddy и внешнего reverse proxy валидны, но для реальной проверки нужны публичный DNS и доступные порты 80/443.
- Production release hardening ещё не завершён.
- Dev-пароли в исходных шаблонах не предназначены для production; setup создаёт случайные значения.

## Следующие этапы

1. Проверить публичный DNS/ACME на внешнем сервере.
2. Настроить подписанный release.

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
