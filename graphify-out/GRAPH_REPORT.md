# Graph Report - .  (2026-09-01)

## Corpus Check
- Corpus is ~22,981 words - fits in a single context window. You may not need a graph.

## Summary
- 397 nodes · 959 edges · 19 communities (17 shown, 2 thin omitted)
- Extraction: 83% EXTRACTED · 17% INFERRED · 0% AMBIGUOUS · INFERRED: 163 edges (avg confidence: 0.81)
- Token cost: 0 input · 65,122 output

## Community Hubs (Navigation)
- Application Configuration
- Registry Reconciliation
- Settings Storage & Models
- Device Handlers & Views
- Settings Handlers
- Technitium DNS Client
- OIDC Authentication
- UniFi Client Reconciliation
- Config File Schema
- Sync Engine Tests
- Identity Management
- MAC & Naming Utilities
- Identity Member Selection
- App Bootstrap & Web Assets
- UniFi HTTP Client
- Network Liveness Checks
- Logging Setup
- Go Module

## God Nodes (most connected - your core abstractions)
1. `Reconcile()` - 35 edges
2. `SettingsHandlers` - 26 edges
3. `makeClient()` - 23 edges
4. `Handlers` - 19 edges
5. `fixedZone()` - 19 edges
6. `Engine` - 17 edges
7. `newTestEngine()` - 16 edges
8. `Client` - 15 edges
9. `Config` - 13 edges
10. `testDB()` - 13 edges

## Surprising Connections (you probably didn't know these)
- `Settings: Identities Page` --semantically_similar_to--> `DNS Sync Configuration (config.example.yaml)`  [INFERRED] [semantically similar]
  internal/api/web/templates/settings_identities.html → config.example.yaml
- `run()` --calls--> `Load()`  [INFERRED]
  cmd/netreg/main.go → internal/config/config.go
- `run()` --calls--> `Key()`  [INFERRED]
  cmd/netreg/main.go → internal/settings/settings.go
- `run()` --calls--> `LoadTechnitium()`  [INFERRED]
  cmd/netreg/main.go → internal/settings/settings.go
- `run()` --calls--> `SeedFromConfig()`  [INFERRED]
  cmd/netreg/main.go → internal/settings/settings.go

## Import Cycles
- None detected.

## Hyperedges (group relationships)
- **Settings Sub-navigation Page Group** — internal_api_web_templates_settings_controllers_content, internal_api_web_templates_settings_general_content, internal_api_web_templates_settings_identities_content, internal_api_web_templates_settings_technitium_content, internal_api_web_templates_settings_zones_content [EXTRACTED 1.00]
- **Application Seed Configuration Schema** — config_example_database, config_example_unifi, config_example_technitium, config_example_dns, config_example_oidc, config_example_server [EXTRACTED 1.00]
- **Device Tracking UI Flow** — internal_api_web_templates_layout_layout, internal_api_web_templates_dashboard_content, internal_api_web_templates_device_content, internal_api_web_templates_events_content [INFERRED 0.85]

## Communities (19 total, 2 thin omitted)

### Community 0 - "Application Configuration"
Cohesion: 0.10
Nodes (28): Config, DatabaseConfig, DNSConfig, OIDCConfig, ServerConfig, TechnitiumConfig, UnifiConfig, applyEnvOverrides() (+20 more)

### Community 1 - "Registry Reconciliation"
Cohesion: 0.18
Nodes (34): makeClient(), estimateLeaseExpiry(), Time, HasValidIP(), Reconcile(), shouldRemove(), fixedZone(), T (+26 more)

### Community 2 - "Settings Storage & Models"
Cohesion: 0.14
Nodes (31): AEAD, AppSettings, TechnitiumSettings, UnifiController, UnifiNetwork, controllerView, pageData, Duration (+23 more)

### Community 3 - "Device Handlers & Views"
Cohesion: 0.14
Nodes (16): Device, SyncEvent, SyncEventAction, deviceView, DNSClient, DNSClientFactory, Engine, Handlers (+8 more)

### Community 4 - "Settings Handlers"
Cohesion: 0.21
Nodes (10): API, New(), Context, DB, Handler, SettingsHandlers, Logger, Request (+2 more)

### Community 5 - "Technitium DNS Client"
Cohesion: 0.15
Nodes (16): cloneValues(), Context, Mutex, New(), fakeDNS, AddRecordRequest, apiResult, Client (+8 more)

### Community 6 - "OIDC Authentication"
Cohesion: 0.14
Nodes (16): Authenticator, CookieStore, IDTokenVerifier, extractGroups(), Context, Handler, Request, ResponseWriter (+8 more)

### Community 7 - "UniFi Client Reconciliation"
Cohesion: 0.12
Nodes (17): uniFiDisplayName(), Context, Context, API, toNetworkClient(), Context, API, fakeUnifi (+9 more)

### Community 8 - "Config File Schema"
Cohesion: 0.14
Nodes (23): Database Configuration (config.yaml), DNS Sync Configuration (config.yaml), OIDC Configuration (config.yaml), Server Configuration (config.yaml), Technitium Seed Configuration (config.yaml), UniFi Seed Configuration (config.yaml), Database Configuration (config.example.yaml), DNS Sync Configuration (config.example.yaml) (+15 more)

### Community 9 - "Sync Engine Tests"
Cohesion: 0.32
Nodes (21): Engine, DB, DNSClient, Logger, T, newTestEngine(), seedAppSettings(), seedController() (+13 more)

### Community 10 - "Identity Management"
Cohesion: 0.19
Nodes (8): Identity, IdentityMember, identityView, SettingsHandlers, Request, ResponseWriter, Time, ServeMux

### Community 11 - "MAC & Naming Utilities"
Cohesion: 0.18
Nodes (18): Normalize(), OUI(), Suffix(), Disambiguate(), fallbackName(), isGenericName(), Resolve(), SanitizeLabel() (+10 more)

### Community 12 - "Identity Member Selection"
Cohesion: 0.20
Nodes (13): Duration, Time, SelectActive(), T, onlyAlive(), TestSelectActiveFallsBackToHighestPriorityWhenNoneAlive(), TestSelectActiveFallsThroughToNextPriorityWhenFirstIsDead(), TestSelectActivePicksHighestPriorityAliveMember() (+5 more)

### Community 13 - "App Bootstrap & Web Assets"
Cohesion: 0.21
Nodes (10): Logger, main(), run(), FS, Template, Static(), Templates(), DB (+2 more)

### Community 14 - "UniFi HTTP Client"
Cohesion: 0.43
Nodes (4): Context, Mutex, API, New()

### Community 15 - "Network Liveness Checks"
Cohesion: 0.67
Nodes (6): Context, Duration, IsAlive(), New(), pingAlive(), tcpAlive()

## Knowledge Gaps
- **11 isolated node(s):** `github.com/Ohgwen/on-netreg`, `DNSClient`, `NetworkInfo`, `Engine`, `loginResponse` (+6 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **2 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `Reconcile()` connect `Registry Reconciliation` to `Application Configuration`, `MAC & Naming Utilities`, `Device Handlers & Views`, `UniFi Client Reconciliation`?**
  _High betweenness centrality (0.236) - this node is a cross-community bridge._
- **Why does `NetworkClient` connect `UniFi Client Reconciliation` to `Registry Reconciliation`, `MAC & Naming Utilities`?**
  _High betweenness centrality (0.111) - this node is a cross-community bridge._
- **Why does `pageData` connect `Settings Storage & Models` to `Identity Management`, `Device Handlers & Views`, `Settings Handlers`, `Technitium DNS Client`?**
  _High betweenness centrality (0.105) - this node is a cross-community bridge._
- **Are the 24 inferred relationships involving `Reconcile()` (e.g. with `Disambiguate()` and `Resolve()`) actually correct?**
  _`Reconcile()` has 24 INFERRED edges - model-reasoned connections that need verification._
- **Are the 21 inferred relationships involving `makeClient()` (e.g. with `TestResolveFallsBackOnGenericName()` and `TestResolveFallsBackWhenBothBlank()`) actually correct?**
  _`makeClient()` has 21 INFERRED edges - model-reasoned connections that need verification._
- **What connects `github.com/Ohgwen/on-netreg`, `DNSClient`, `NetworkInfo` to the rest of the system?**
  _11 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `Application Configuration` be split into smaller, more focused modules?**
  _Cohesion score 0.09871794871794871 - nodes in this community are weakly interconnected._