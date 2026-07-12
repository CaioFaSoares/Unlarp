# DAEMON.md — Design do daemon local do Unlarp

> Status: **design aprovado para implementação futura** — nada aqui está implementado.
> Este documento descreve como o CLI local vai evoluir para um modelo com daemon.

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

1. `internal/daemonapi` (tipos + contrato, espelho do agentapi) e o servidor
   `unlarp daemon` hospedando o código de engine que hoje vive em `cmd/sync.go`
   e `internal/tui/app.go` (`startSyncEngine` é praticamente o corpo do handler
   `POST /v1/syncs`).
2. Cliente + auto-start; `sync start/stop/status` chamando a API.
3. TUI consumindo `/v1/syncs` e `/v1/logs` (remoção dos guards de Owner/PID).
4. Túneis.
