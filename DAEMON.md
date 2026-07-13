# DAEMON.md — Design do daemon local do Unlarp

> Status: **implementado (fases 1–4 da seção 8)** — é **opt-in**, não o
> padrão. Sem ativação, CLI e TUI continuam rodando as engines in-process,
> exatamente como antes deste documento.

## 0. Como ativar hoje

### CLI

- `unlarp sync start --daemon --remote-dir <dir> [--local-dir <dir>] [host]`
  cria o sync no daemon em vez de in-process. Sobe o daemon sozinho (fork/exec
  desanexado) se ele ainda não estiver de pé. O sync sobrevive ao fechamento
  do terminal.
- `unlarp tunnel <portas> [host] --daemon` faz o mesmo para túneis.
- `unlarp sync status` / `sync stop <id>` / `tunnel list` / `tunnel stop <id>`
  (e `stop --all`) já detectam sozinhos o que está no daemon e mesclam com o
  que está in-process — nenhuma flag extra necessária para consultar ou parar.
- Gerência direta do processo do daemon (raramente precisa ser chamado à mão,
  o auto-start acima já cobre o uso comum):
  - `unlarp daemon` — roda o servidor em foreground.
  - `unlarp daemon status` — versão, protocolo, PID, uptime, nº de syncs.
  - `unlarp daemon stop` — SIGTERM + espera o socket sumir.

### TUI

- Aba **Dashboard**, tecla **`D`** — liga/desliga `daemon.enabled` em
  `~/.unlarp.yaml`; o estado atual aparece na própria aba ("Daemon local:
  ativado/desativado (D para alternar)").
- Com o toggle ligado, **novos syncs criados pela TUI** (prompt `s`) passam a
  ser criados no daemon; a aba **Logs** passa a mesclar os eventos do daemon
  (`GET /v1/logs`) no polling de 1s já existente.
- **Túneis criados pela TUI continuam in-process** mesmo com o toggle ligado
  — por enquanto só o CLI (`unlarp tunnel --daemon`) cria túneis no daemon. A
  rota condicional equivalente à da Fase 3 ainda não chegou em
  `createTunnelLive` (`internal/tui/app.go`).
- Syncs de outros processos (do daemon ou de um `unlarp sync start` em outro
  terminal) continuam só sendo **exibidos**, nunca recriados: os guards de
  `Owner`/`PID`/`Alive()` seguem em vigor, e um sync do daemon aparece como
  "externo (daemon, pid <pid>)" na aba Syncs.

## 1. Motivação

Hoje as engines de sync vivem **dentro do processo que as criou**:

- `unlarp sync start` roda em foreground — fechar o terminal mata o sync.
- Syncs criados pela TUI morrem quando a TUI fecha.
- TUI e CLI são dois escritores independentes do `state.json` (last-writer-wins
  no arquivo inteiro): um pode sobrescrever silenciosamente o registro do outro.
- A TUI precisa de guards (`Owner`/`PID`/`Alive()`) para não duplicar a engine
  de um sync que outro processo mantém vivo — complexidade que só existe porque
  não há um dono único.

Um daemon local resolve os quatro problemas: **um único processo hospeda todas
as engines de sync e os tunnel managers**, e TUI/CLI viram clientes finos.

## 2. Arquitetura proposta

```
┌────────────┐   HTTP sobre unix socket    ┌──────────────────────────┐
│ unlarp tui │ ──────────────────────────► │ unlarp daemon            │
├────────────┤   ~/.unlarp/daemon.sock     │  - engines de sync       │
│ unlarp sync│ ──────────────────────────► │  - tunnel managers       │
│ (CLI)      │                             │  - watchers (fsnotify)   │
└────────────┘                             │  - dono único do state   │
                                           └───────────┬──────────────┘
                                                       │ SSH/SFTP (como hoje)
                                                       ▼
                                                   hosts remotos
```

- **Transporte**: HTTP sobre unix socket local `~/.unlarp/daemon.sock` — o
  mesmo padrão já usado pelo `unlarp-agent` no servidor (`internal/agent/client.go`
  com `http.Transport{DialContext: unix}` e `internal/agentapi` como contrato).
  Reusar esse código é o motivo principal da escolha: zero dependência nova,
  auth por permissão de arquivo (0600, mesmo usuário), nada de porta TCP.
- **Um daemon por usuário**, multi-host por dentro (as engines já são
  indexadas por host na TUI hoje; o daemon herda esse modelo).
- O daemon passa a ser **o único escritor** de `state.json` (fim do
  last-writer-wins entre TUI e CLI).

## 3. API mínima (v1)

Prefixo `/v1`, JSON, versionada com `Protocol` int como no agentapi:

| Método e rota              | Função                                              |
|----------------------------|-----------------------------------------------------|
| `GET  /v1/info`            | handshake: versão, protocolo, uptime                |
| `GET  /v1/syncs`           | lista de syncs + progresso (substitui `GetProgress` local) |
| `POST /v1/syncs`           | criar sync `{host, localDir, remoteDir, mode, project}` |
| `DELETE /v1/syncs/{id}`    | parar e remover sync                                |
| `POST /v1/syncs/{id}/pause` e `/resume` | GitGuard/resolução continua funcionando |
| `GET  /v1/tunnels` / `POST` / `DELETE` | mesmo modelo para túneis              |
| `GET  /v1/logs?since=<seq>`| log estruturado com cursor (a TUI faz polling)      |

Progresso na TUI: **polling de 1s** no `tickCmd` já existente — sem
push/streaming na v1 (o tick já existe e 1s é a cadência atual da UI).

## 4. Ciclo de vida

**Recomendado: auto-start no primeiro uso.**

1. Cliente tenta conectar no socket.
2. Falhou → adquire lock (`flock` em `~/.unlarp/daemon.lock`), faz
   fork/exec de `unlarp daemon` desanexado, espera o socket aparecer (timeout
   curto), conecta.
3. `unlarp daemon stop` para desligar; `unlarp daemon status` para inspecionar.

launchd (macOS) / systemd user unit ficam **documentados como opcionais** para
quem quiser o daemon sempre de pé — não são requisito. Auto-start no primeiro
comando é multiplataforma e não exige instalação.

## 5. Migração (convivência com o modelo atual)

- `SyncEntry.Owner` ganha o valor `"daemon"` (o campo já existe: `cli`/`tui`).
- Período de convivência: TUI e CLI continuam sabendo rodar engines localmente;
  quando o daemon está de pé, **criam via API** em vez de in-process.
- Syncs órfãos (`Owner` antigo com `!Alive()`) são **adotados** pelo daemon no
  boot — mesma semântica do `restoreSyncsCmd` atual da TUI.
- `unlarp sync stop` vira `DELETE /v1/syncs/{id}` (não precisa mais de SIGTERM
  em PID nem do caso especial "owner é a TUI, não mate").
- Quando o daemon vira padrão, os guards de `Owner`/`PID` na TUI podem ser
  removidos (a TUI deixa de criar engines).

## 6. Logs

O daemon vira **a fonte de verdade dos logs**:

- Arquivo `~/.unlarp/daemon.log`, formato `RFC3339 nível mensagem` (uma linha),
  com **rotação simples por tamanho** (rename para `.1` ao passar de ~5 MB,
  mantendo 1 geração — sem lib nova).
- Buffer circular em memória com número de sequência; `GET /v1/logs?since=`
  devolve o incremento — é isso que a aba Logs da TUI passa a consumir (o
  buffer `m.logs` local fica só para eventos da própria UI).
- Os audit logs por sync (`sync_audit_<host>_<id>.log`) continuam como estão.

## 7. Fora de escopo da v1

- Daemon remoto / multi-usuário / TLS no socket.
- Streaming de eventos (long-poll/SSE) — polling de 1s cobre a TUI.
- UI web — ver REMOTE.md.
- Migração automática de state antigo além da adoção de órfãos.

## 8. Ordem de implementação sugerida

> As 4 fases abaixo estão implementadas (ver seção 0 para como ativar). O
> daemon ficou **opt-in** neste ciclo — diferente do que a fase 3 original
> cogitava, os guards de Owner/PID/Alive() da TUI **não** foram removidos,
> já que in-process continua sendo o caminho padrão quando o toggle está
> desligado.

1. ✅ `internal/daemonapi` (tipos + contrato, espelho do agentapi) e o servidor
   `unlarp daemon` hospedando o código de engine que antes vivia em
   `cmd/sync.go` e `internal/tui/app.go` (`startSyncEngine` virou o corpo do
   handler `POST /v1/syncs`, em `internal/daemon/registry.go`).
2. ✅ Cliente + auto-start; `sync start/stop/status --daemon` chamando a API.
3. ✅ TUI: toggle opt-in (`D` no Dashboard) roteando `createSyncLiveCmd` para
   o daemon e mesclando `/v1/logs` na aba Logs. Guards de Owner/PID mantidos.
4. ✅ Túneis via daemon, mas só no CLI (`unlarp tunnel --daemon`) — a TUI
   (`createTunnelLive`) ainda cria túneis in-process independente do toggle.
