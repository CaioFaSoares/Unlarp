# Graph Report - .  (2026-07-10)

## Corpus Check
- Corpus is ~40,850 words - fits in a single context window. You may not need a graph.

## Summary
- 552 nodes · 974 edges · 46 communities (34 shown, 12 thin omitted)
- Extraction: 86% EXTRACTED · 14% INFERRED · 0% AMBIGUOUS · INFERRED: 135 edges (avg confidence: 0.8)
- Token cost: 0 input · 29,140 output

## Community Hubs (Navigation)
- [[_COMMUNITY_TUI Dashboard Core|TUI Dashboard Core]]
- [[_COMMUNITY_Sync Engine Internals|Sync Engine Internals]]
- [[_COMMUNITY_Compose Orchestration|Compose Orchestration]]
- [[_COMMUNITY_Tunnel CLI Commands|Tunnel CLI Commands]]
- [[_COMMUNITY_Config Store|Config Store]]
- [[_COMMUNITY_CLI Entrypoint Bootstrap|CLI Entrypoint Bootstrap]]
- [[_COMMUNITY_Port Forwarding|Port Forwarding]]
- [[_COMMUNITY_SSH Session Setup|SSH Session Setup]]
- [[_COMMUNITY_TUI SyncProject Panels|TUI Sync/Project Panels]]
- [[_COMMUNITY_Session Manager|Session Manager]]
- [[_COMMUNITY_Tunnel Manager Lifecycle|Tunnel Manager Lifecycle]]
- [[_COMMUNITY_Project README Docs|Project README Docs]]
- [[_COMMUNITY_Remote File Watcher|Remote File Watcher]]
- [[_COMMUNITY_Sync Snapshot Diffing|Sync Snapshot Diffing]]
- [[_COMMUNITY_Local File Watcher|Local File Watcher]]
- [[_COMMUNITY_Directory Picker UI|Directory Picker UI]]
- [[_COMMUNITY_Git Status Helpers|Git Status Helpers]]
- [[_COMMUNITY_TUI Tmux Session State|TUI Tmux Session State]]
- [[_COMMUNITY_Docker Compose Workspace|Docker Compose Workspace]]
- [[_COMMUNITY_TUI Session Launch|TUI Session Launch]]
- [[_COMMUNITY_Forward Direction Tests|Forward Direction Tests]]
- [[_COMMUNITY_Sync Conflict Resolution|Sync Conflict Resolution]]
- [[_COMMUNITY_SSH Config Import|SSH Config Import]]
- [[_COMMUNITY_TUI Main Panel Render|TUI Main Panel Render]]
- [[_COMMUNITY_TUI Logs Render|TUI Logs Render]]
- [[_COMMUNITY_TUI Onboarding Render|TUI Onboarding Render]]
- [[_COMMUNITY_TUI Tunnels Render|TUI Tunnels Render]]
- [[_COMMUNITY_Machine Entrypoint Script|Machine Entrypoint Script]]
- [[_COMMUNITY_Local SSH Connect Script|Local SSH Connect Script]]
- [[_COMMUNITY_Local SSH Setup Script|Local SSH Setup Script]]
- [[_COMMUNITY_Remote SSH Setup Script|Remote SSH Setup Script]]
- [[_COMMUNITY_Tunnel Shell Script|Tunnel Shell Script]]
- [[_COMMUNITY_Exec Command Doc|Exec Command Doc]]

## God Nodes (most connected - your core abstractions)
1. `AppModel` - 46 edges
2. `Engine` - 42 edges
3. `Forwarder` - 27 edges
4. `Manager` - 22 edges
5. `Store` - 21 edges
6. `Manager` - 21 edges
7. `runSyncStart()` - 18 edges
8. `Client` - 18 edges
9. `Cmd` - 18 edges
10. `runComposeUp()` - 16 edges

## Surprising Connections (you probably didn't know these)
- `resolveComposeProject()` --calls--> `getActiveHost()`  [INFERRED]
  cli/cmd/compose.go → cli/cmd/root.go
- `resolveComposeProject()` --calls--> `getHostConfig()`  [INFERRED]
  cli/cmd/compose.go → cli/cmd/root.go
- `connectForCompose()` --calls--> `NewClient()`  [INFERRED]
  cli/cmd/compose.go → cli/internal/ssh/client.go
- `runComposeUp()` --calls--> `printTunnelStatus()`  [INFERRED]
  cli/cmd/compose.go → cli/cmd/tunnel.go
- `runComposeUp()` --calls--> `NewStore()`  [INFERRED]
  cli/cmd/compose.go → cli/internal/config/store.go

## Import Cycles
- None detected.

## Hyperedges (group relationships)
- **Unlarp CLI command surface (config, setup, use, connect/exec, sync, tunnel)** — readme_unlarp_config, readme_unlarp_setup, readme_unlarp_use, readme_unlarp_connect, readme_unlarp_sync, readme_unlarp_tunnel [EXTRACTED 0.90]
- **Coolify named volumes mapped to workspace container paths** — dockercompose_workspace_data_volume, dockercompose_workspace_nix_store_volume, dockercompose_workspace_home_volume, dockercompose_workspace_docker_volume, readme_coolify_deploy [EXTRACTED 0.90]
- **Workspace Container Persistent State Volumes** — docker_compose_workspace_service, docker_compose_workspace_data_volume, docker_compose_workspace_nix_store_volume, docker_compose_workspace_home_volume, docker_compose_workspace_docker_volume [INFERRED 0.85]

## Communities (46 total, 12 thin omitted)

### Community 0 - "TUI Dashboard Core"
Cohesion: 0.07
Nodes (36): Client, Cmd, Direction, Host, Manager, Model, Msg, Mutex (+28 more)

### Community 1 - "Sync Engine Internals"
Cohesion: 0.06
Nodes (30): FileMode, Client, IgnoreMatcher, Mutex, RWMutex, RWMutex, ConflictStrategy, WriteFileAtomic() (+22 more)

### Community 2 - "Compose Orchestration"
Cohesion: 0.12
Nodes (28): Client, Command, Host, Project, T, composeExec(), connectForCompose(), printComposeServices() (+20 more)

### Community 3 - "Tunnel CLI Commands"
Cohesion: 0.10
Nodes (23): Host, Command, Command, Manager, portMapping, getActiveHost(), getHostConfig(), generateSyncID() (+15 more)

### Community 4 - "Config Store"
Cohesion: 0.11
Nodes (15): Duration, Host, Project, Config, Host, Project, SessionConfig, SSHGlobalConfig (+7 more)

### Community 5 - "CLI Entrypoint Bootstrap"
Cohesion: 0.10
Nodes (15): T, Client, FileMode, T, main(), Execute(), findPublicKey(), FileInfo (+7 more)

### Community 6 - "Port Forwarding"
Cohesion: 0.10
Nodes (13): CancelFunc, Client, Context, RWMutex, Time, Conn, Int32, Int64 (+5 more)

### Community 7 - "SSH Session Setup"
Cohesion: 0.12
Nodes (16): AuthMethod, Host, Duration, Host, Session, ClientConfig, showHostStatus(), Signer (+8 more)

### Community 8 - "TUI Sync/Project Panels"
Cohesion: 0.14
Nodes (18): Duration, Project, SyncEntry, AppModel, SyncEntry, AppModel, FileProgress, pendingSync (+10 more)

### Community 9 - "Session Manager"
Cohesion: 0.14
Nodes (11): RWMutex, Session, SyncEntry, Time, Manager, NewManager(), Session, State (+3 more)

### Community 10 - "Tunnel Manager Lifecycle"
Cohesion: 0.14
Nodes (14): CancelFunc, Client, Context, Direction, Duration, Host, RWMutex, Time (+6 more)

### Community 11 - "Project README Docs"
Cohesion: 0.11
Nodes (20): Charm Stack (bubbletea, lipgloss, bubbles), Deploy no Coolify (Servidor Remoto), Docker-in-Docker (DinD), Nix Flakes, Onboarding Wizard, SSH config import (--from-ssh-config), ~/.unlarp/state.json, Three-way Reconciliation algorithm (+12 more)

### Community 12 - "Remote File Watcher"
Cohesion: 0.20
Nodes (11): CancelFunc, Client, Context, Duration, IgnoreMatcher, Mutex, Time, WaitGroup (+3 more)

### Community 13 - "Sync Snapshot Diffing"
Cohesion: 0.17
Nodes (13): Client, FileMode, IgnoreMatcher, Time, T, FileEntry, Snapshot, CalculateRemoteHash() (+5 more)

### Community 14 - "Local File Watcher"
Cohesion: 0.22
Nodes (9): CancelFunc, Context, Duration, IgnoreMatcher, Mutex, WaitGroup, Watcher, NewLocalWatcher() (+1 more)

### Community 15 - "Directory Picker UI"
Cohesion: 0.30
Nodes (8): Client, Cmd, Model, Msg, DirPicker, ChooseLocalDir(), ChooseRemoteDir(), NewDirPicker()

### Community 16 - "Git Status Helpers"
Cohesion: 0.28
Nodes (8): AheadBehind, Client, Time, AheadBehind, GetRemoteGitInfo(), PullLocal(), shellQuote(), RemoteGitInfo

### Community 17 - "TUI Tmux Session State"
Cohesion: 0.40
Nodes (3): Duration, Time, TmuxSession

### Community 18 - "Docker Compose Workspace"
Cohesion: 0.33
Nodes (6): ./machine Dockerfile Build Context, workspace-data Volume, workspace-docker Volume, workspace-home Volume, workspace-nix-store Volume, workspace Docker Compose Service

### Community 20 - "Forward Direction Tests"
Cohesion: 0.67
Nodes (3): T, TestDialPeerRemoteDirection(), TestDirectionString()

### Community 21 - "Sync Conflict Resolution"
Cohesion: 0.67
Nodes (3): FileEntry, ResolveConflict(), ConflictStrategy

## Knowledge Gaps
- **100 isolated node(s):** `Service`, `Host`, `Host`, `Manager`, `Publisher` (+95 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **12 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `Manager` connect `Tunnel Manager Lifecycle` to `Sync Engine Internals`, `Tunnel CLI Commands`?**
  _High betweenness centrality (0.143) - this node is a cross-community bridge._
- **Why does `runSyncStart()` connect `Tunnel CLI Commands` to `TUI Dashboard Core`, `Sync Engine Internals`, `Compose Orchestration`, `CLI Entrypoint Bootstrap`, `SSH Session Setup`, `Remote File Watcher`, `Local File Watcher`, `Directory Picker UI`?**
  _High betweenness centrality (0.132) - this node is a cross-community bridge._
- **Why does `NewStore()` connect `TUI Dashboard Core` to `Compose Orchestration`, `Tunnel CLI Commands`, `Config Store`?**
  _High betweenness centrality (0.116) - this node is a cross-community bridge._
- **What connects `Service`, `Host`, `Host` to the rest of the system?**
  _102 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `TUI Dashboard Core` be split into smaller, more focused modules?**
  _Cohesion score 0.07453416149068323 - nodes in this community are weakly interconnected._
- **Should `Sync Engine Internals` be split into smaller, more focused modules?**
  _Cohesion score 0.06170598911070781 - nodes in this community are weakly interconnected._
- **Should `Compose Orchestration` be split into smaller, more focused modules?**
  _Cohesion score 0.12063492063492064 - nodes in this community are weakly interconnected._