# Архитектура Lore Platform

## Система в целом

```mermaid
flowchart LR
    Guest["Гость<br/>публичное чтение"]
    User["Пользователь<br/>React WebUI"]
    Admin["Администратор<br/>пользователи, репозитории, ACL"]
    Explorer["Windows Explorer<br/>C++ COM + C# Companion"]

    subgraph Edge["Веб и интеграционный контур"]
        Caddy["Caddy<br/>единый HTTPS вход"]
        CP["Go Control Plane<br/>HTTP :8080 · gRPC :9090"]
        KC["Keycloak<br/>OIDC · PKCE · registration"]
        AD[("Active Directory<br/>LDAP / LDAPS")]
    end

    subgraph Data["Данные и версионирование"]
        PG[("PostgreSQL<br/>users · grants · sessions · audit")]
        Lore["Lore Server<br/>Repository / Revision / Storage"]
        CAS[("Content Addressable Storage<br/>репозитории и ревизии")]
    end

    Guest -->|"HTTPS"| Caddy
    User -->|"HTTPS + session cookie"| Caddy
    Admin -->|"HTTPS + CSRF"| Caddy
    Caddy --> CP
    Caddy -->|"/auth"| KC
    CP <-->|"OIDC"| KC
    KC <-->|"LDAP / LDAPS"| AD
    CP <-->|"SQL"| PG
    CP -->|"gRPC RepositoryCreate<br/>service identity + creator"| Lore
    Lore -->|"gRPC UserService<br/>RepositoryRegistryService + user JWT"| CP
    Lore <--> CAS
    Explorer -->|"LoreVcs SDK<br/>Clone · Sync · Commit"| Lore
    Explorer -.->|"browser OIDC"| KC
    Explorer -->|"subject token + resource<br/>ACL-aware token exchange"| CP

    classDef person fill:#2563eb,stroke:#93c5fd,color:#fff,stroke-width:1px;
    classDef service fill:#172554,stroke:#60a5fa,color:#eff6ff,stroke-width:1px;
    classDef data fill:#3b0764,stroke:#c084fc,color:#faf5ff,stroke-width:1px;
    classDef windows fill:#14532d,stroke:#4ade80,color:#f0fdf4,stroke-width:1px;
    class Guest,User,Admin person;
    class CP,KC,AD,Caddy service;
    class PG,Lore,CAS data;
    class Explorer windows;
```

## Создание настоящего репозитория

```mermaid
sequenceDiagram
    autonumber
    actor A as Владелец
    participant W as React WebUI
    participant C as Control Plane
    participant P as PostgreSQL
    participant L as Lore Server

    A->>W: Имя, описание, видимость
    W->>C: POST /admin/repositories + CSRF
    C->>C: Проверка admin и имени
    C->>C: Сохранить client-supplied UUID для идемпотентного retry
    C->>L: RepositoryCreate(id, default_branch_id, creator)
    alt Lore подтвердил создание
        L-->>C: Repository
        C->>P: Зафиксировать репозиторий, owner grant и public read
        C-->>W: 201 Created
        W-->>A: Репозиторий доступен
    else Сбой Lore или конфликт
        L-->>C: AlreadyExists / Unavailable / ошибка
        C-->>W: Понятная ошибка без ложного успеха
        Note over C,P: Повтор использует тот же ID;<br/>незавершённую операцию обрабатывает reconciliation
    end
```

## Вычисление доступа

```mermaid
flowchart TD
    Request["Запрос к репозиторию"] --> Identity{"Есть действующая<br/>OIDC-идентичность?"}
    Identity -->|"Нет"| Anonymous["Гость<br/>только anonymous read"]
    Identity -->|"Да"| Disabled{"Аккаунт<br/>отключён?"}
    Disabled -->|"Да"| Deny["Deny"]
    Disabled -->|"Нет"| Admin{"Глобальная<br/>роль admin?"}
    Admin -->|"Да"| Owner["owner"]
    Admin -->|"Нет"| Grants["Объединить direct user,<br/>AD group и authenticated grants"]
    Grants --> Highest["Максимум:<br/>none < read < write < owner"]
    Anonymous --> Public{"Публичный<br/>anonymous read?"}
    Public -->|"Да"| Read["read"]
    Public -->|"Нет"| Hide["404 / PermissionDenied"]
    Highest --> Decision{"Достаточно прав<br/>для операции?"}
    Read --> Allow["Разрешить чтение"]
    Owner --> Allow
    Decision -->|"Да"| Allow
    Decision -->|"Нет"| Deny

    classDef deny fill:#7f1d1d,stroke:#f87171,color:#fff;
    classDef allow fill:#14532d,stroke:#4ade80,color:#fff;
    classDef decision fill:#713f12,stroke:#facc15,color:#fff;
    class Deny,Hide deny;
    class Allow,Read,Owner allow;
    class Identity,Disabled,Admin,Public,Decision decision;
```

## Границы доверия

- Bearer-токены не передаются через Windows request files и не записываются в overlay cache.
- WebUI использует opaque session cookie и CSRF для изменяющих запросов.
- gRPC `:9090` предназначен для внутренней сети; публичный вход — HTTPS через reverse proxy.
- Гость не получает write- или owner-grant; `none` не является deny-правилом.
- Anonymous Clone использует `urn:lore:grant-type:anonymous`: control plane разрешает имя или ID только при anonymous Read и подписывает краткоживущий read-only JWT. Имена приватных репозиториев не раскрываются.
- Native logout проверяет подпись и subject JWT; истёкший токен принимается только для завершения сессии и не даёт доступа к данным.
- AD provisioning ищет пользователя в LDAP по `sAMAccountName`, при необходимости сопоставляет по email с federated Keycloak-аккаунтом и загружает группы до первого входа.
- Секреты Keycloak, PostgreSQL и service account задаются deployment-окружением, а не фиксируются в исходниках.
