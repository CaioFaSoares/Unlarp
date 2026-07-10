# Graph Report - .  (2026-07-10)

## Corpus Check
- Corpus is ~35,128 words - fits in a single context window. You may not need a graph.

## Summary
- 471 nodes · 837 edges · 35 communities (29 shown, 6 thin omitted)
- Extraction: 87% EXTRACTED · 13% INFERRED · 0% AMBIGUOUS · INFERRED: 106 edges (avg confidence: 0.8)
- Token cost: 0 input · 0 output

## Community Hubs (Navigation)
- [[_COMMUNITY_TUI App Model & Watchers|TUI App Model & Watchers]]
- [[_COMMUNITY_Sync Engine & Conflict Resolution|Sync Engine & Conflict Resolution]]
- [[_COMMUNITY_CLI Command Root & SyncTunnel Commands|CLI Command Root & Sync/Tunnel Commands]]
- [[_COMMUNITY_Config Store & Models|Config Store & Models]]
- [[_COMMUNITY_Tunnel Forwarding|Tunnel Forwarding]]
- [[_COMMUNITY_Setup & Main Entrypoint|Setup & Main Entrypoint]]
- [[_COMMUNITY_SSH Client & Auth|SSH Client & Auth]]
- [[_COMMUNITY_Ignore Matching & Snapshots|Ignore Matching & Snapshots]]
- [[_COMMUNITY_TUI Projects & Syncs Views|TUI Projects & Syncs Views]]
- [[_COMMUNITY_Tunnel Manager|Tunnel Manager]]
- [[_COMMUNITY_Session Manager|Session Manager]]
- [[_COMMUNITY_Remote Watcher|Remote Watcher]]
- [[_COMMUNITY_Local Watcher|Local Watcher]]
- [[_COMMUNITY_Directory Picker UI|Directory Picker UI]]
- [[_COMMUNITY_Git Info|Git Info]]
- [[_COMMUNITY_TUI Launch Command|TUI Launch Command]]
- [[_COMMUNITY_Tunnel Forward Tests|Tunnel Forward Tests]]
- [[_COMMUNITY_SSH Config Parsing|SSH Config Parsing]]
- [[_COMMUNITY_TUI Main Panel Rendering|TUI Main Panel Rendering]]
- [[_COMMUNITY_TUI Logs Rendering|TUI Logs Rendering]]
- [[_COMMUNITY_TUI Onboarding Rendering|TUI Onboarding Rendering]]
- [[_COMMUNITY_TUI Tunnels Rendering|TUI Tunnels Rendering]]

## God Nodes (most connected - your core abstractions)
1. `AppModel` - 46 edges
2. `Engine` - 39 edges
3. `Forwarder` - 27 edges
4. `Manager` - 22 edges
5. `Store` - 21 edges
6. `Manager` - 21 edges
7. `runSyncStart()` - 18 edges
8. `Client` - 18 edges
9. `Cmd` - 18 edges
10. `SFTPClient` - 15 edges

## Surprising Connections (you probably didn't know these)
- `showHostStatus()` --calls--> `StatusLine()`  [INFERRED]
  cmd/status.go → internal/ui/printer.go
- `runSyncStart()` --calls--> `NewClient()`  [INFERRED]
  cmd/sync.go → internal/ssh/client.go
- `runSyncStart()` --calls--> `NewSFTPClient()`  [INFERRED]
  cmd/sync.go → internal/ssh/sftp.go
- `runSyncStart()` --calls--> `NewEngine()`  [INFERRED]
  cmd/sync.go → internal/sync/engine.go
- `runSyncStart()` --calls--> `ChooseLocalDir()`  [INFERRED]
  cmd/sync.go → internal/ui/dirpicker.go

## Import Cycles
- None detected.

## Communities (35 total, 6 thin omitted)

### Community 0 - "TUI App Model & Watchers"
Cohesion: 0.07
Nodes (35): Config, DirPicker, Engine, Client, Cmd, Direction, Host, Manager (+27 more)

### Community 1 - "Sync Engine & Conflict Resolution"
Cohesion: 0.07
Nodes (27): ConflictStrategy, FileEntry, WriteFileAtomic(), GitGuard, FileMode, Client, IgnoreMatcher, Mutex (+19 more)

### Community 2 - "CLI Command Root & Sync/Tunnel Commands"
Cohesion: 0.09
Nodes (26): portMapping, getActiveHost(), getHostConfig(), Host, generateSyncID(), Command, runSyncStart(), runSyncStatus() (+18 more)

### Community 3 - "Config Store & Models"
Cohesion: 0.11
Nodes (15): Config, Host, Project, SessionConfig, SSHGlobalConfig, Store, SyncConfig, TunnelConfig (+7 more)

### Community 4 - "Tunnel Forwarding"
Cohesion: 0.10
Nodes (13): Conn, Int32, Int64, CancelFunc, Client, Context, RWMutex, Time (+5 more)

### Community 5 - "Setup & Main Entrypoint"
Cohesion: 0.12
Nodes (11): Execute(), findPublicKey(), FileInfo, TestWriteFileAtomic(), T, Client, FileMode, main() (+3 more)

### Community 6 - "SSH Client & Auth"
Cohesion: 0.12
Nodes (16): AuthMethod, ClientConfig, Host, showHostStatus(), Duration, Host, Session, Signer (+8 more)

### Community 7 - "Ignore Matching & Snapshots"
Cohesion: 0.11
Nodes (19): RWMutex, Client, FileMode, IgnoreMatcher, Time, T, Regexp, FileEntry (+11 more)

### Community 8 - "TUI Projects & Syncs Views"
Cohesion: 0.14
Nodes (16): FileProgress, Project, SyncEntry, AppModel, SyncEntry, AppModel, pendingSync, TmuxSession (+8 more)

### Community 9 - "Tunnel Manager"
Cohesion: 0.14
Nodes (14): Forwarder, ForwarderStatus, CancelFunc, Client, Context, Direction, Duration, Host (+6 more)

### Community 10 - "Session Manager"
Cohesion: 0.17
Nodes (10): RWMutex, Session, SyncEntry, Time, Manager, Session, State, SyncEntry (+2 more)

### Community 11 - "Remote Watcher"
Cohesion: 0.20
Nodes (11): CancelFunc, Client, Context, Duration, IgnoreMatcher, Mutex, Time, WaitGroup (+3 more)

### Community 12 - "Local Watcher"
Cohesion: 0.22
Nodes (9): CancelFunc, Context, Duration, IgnoreMatcher, Mutex, WaitGroup, Watcher, NewLocalWatcher() (+1 more)

### Community 13 - "Directory Picker UI"
Cohesion: 0.30
Nodes (8): Client, Cmd, Model, Msg, DirPicker, ChooseLocalDir(), ChooseRemoteDir(), NewDirPicker()

### Community 14 - "Git Info"
Cohesion: 0.28
Nodes (8): AheadBehind, AheadBehind, GetRemoteGitInfo(), PullLocal(), shellQuote(), RemoteGitInfo, Client, Time

### Community 16 - "Tunnel Forward Tests"
Cohesion: 0.67
Nodes (3): T, TestDialPeerRemoteDirection(), TestDirectionString()

## Knowledge Gaps
- **75 isolated node(s):** `Host`, `Host`, `Manager`, `Host`, `SyncConfig` (+70 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **6 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `Manager` connect `Tunnel Manager` to `Sync Engine & Conflict Resolution`, `CLI Command Root & Sync/Tunnel Commands`?**
  _High betweenness centrality (0.177) - this node is a cross-community bridge._
- **Why does `runSyncStart()` connect `CLI Command Root & Sync/Tunnel Commands` to `Sync Engine & Conflict Resolution`, `Setup & Main Entrypoint`, `SSH Client & Auth`, `Remote Watcher`, `Local Watcher`, `Directory Picker UI`?**
  _High betweenness centrality (0.155) - this node is a cross-community bridge._
- **Why does `NewClient()` connect `SSH Client & Auth` to `TUI App Model & Watchers`, `Tunnel Manager`, `CLI Command Root & Sync/Tunnel Commands`, `Setup & Main Entrypoint`?**
  _High betweenness centrality (0.117) - this node is a cross-community bridge._
- **What connects `Host`, `Host`, `Manager` to the rest of the system?**
  _75 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `TUI App Model & Watchers` be split into smaller, more focused modules?**
  _Cohesion score 0.07459505541346974 - nodes in this community are weakly interconnected._
- **Should `Sync Engine & Conflict Resolution` be split into smaller, more focused modules?**
  _Cohesion score 0.06745098039215686 - nodes in this community are weakly interconnected._
- **Should `CLI Command Root & Sync/Tunnel Commands` be split into smaller, more focused modules?**
  _Cohesion score 0.08780487804878048 - nodes in this community are weakly interconnected._