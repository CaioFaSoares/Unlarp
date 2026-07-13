# Casos de Uso Avançados do Unlarp

O README explica os comandos. Este documento explica os cenários — como
combinar sync, worktree, tunnel, tmux e paste para tirar o máximo do projeto.
Se você só usa `unlarp connect` e mais nada, está deixando a maior parte do
valor na mesa.

## 1. Frota de agentes Claude Code em paralelo, um por branch

O problema: rodar vários agentes na mesma branch é pedir conflito. Rodar cada
um num clone separado é trabalho de setup redobrado.

```bash
unlarp worktree add meu-projeto feat-x -b
unlarp worktree add meu-projeto feat-y -b
unlarp connect --tmux --tmux-session meu-projeto-wt-feat-x
unlarp connect --tmux --tmux-session meu-projeto-wt-feat-y
```

Cada worktree fica em `.claude/worktree-<branch>` **dentro** do repo remoto —
por isso o sync do projeto já espelha as duas no seu Mac, sem configurar
sync novo por branch. `git status` funciona dos dois lados porque a engine
reescreve o `gitdir` entre Mac e container. Resultado: N agentes, N branches,
zero interferência entre eles, e você acompanha todos pela mesma pasta local.

## 2. IDE local, build pesado remoto, sem FUSE

Editar no Mac com a IDE que você já usa, mas rodar build/testes/DinD no
container (mais CPU, mais disco, ambiente reproduzível via Nix).

```bash
unlarp sync start --local-dir . --remote-dir /workspace/meu-projeto
```

Não precisa de SSHFS nem de extensão de kernel no macOS — é reconciliação de
3 vias sobre SFTP, propagação em milissegundos. Ponha um `.unlarpignore`
(sintaxe de `.gitignore`) para `node_modules/`, `dist/`, `.git/` e o sync fica
rápido porque só propaga código-fonte. Artefatos gerados remotamente (build
output, lockfiles atualizados) voltam sozinhos para o Mac.

## 3. Começar no laptop, continuar no celular, sessão nunca morre

O agente está no meio de uma tarefa longa e você precisa sair — mas ele
continua rodando.

- No Mac: `Ctrl+G` desanexa da sessão tmux sem matar nada no container.
- No celular: Termius ou Blink Shell, mesma chave SSH, mesma porta, `tmux
  attach -t <sessão>` — é a **mesma sessão** que o Mac via, não uma cópia.
- Para só espiar sem risco de mexer sem querer: `tmux attach -r` (read-only).
- Sem IP público: Tailscale no container (ver `REMOTE.md`) resolve o acesso
  sem abrir porta nenhuma.

O ganho é que "fechar o laptop" nunca significa "perder o progresso do
agente" — ele mora no container, não na sua máquina.

## 4. Serviços do DinD remoto como se fossem localhost

Um Postgres ou Redis sobem dentro do Docker-in-Docker remoto — mas suas
ferramentas de GUI (TablePlus, RedisInsight, o navegador) rodam no Mac.

```bash
unlarp tunnel 5432,3000:8080,6379 -b
unlarp tunnel list
```

Uma linha encaminha várias portas de uma vez, em background. Não precisa
editar `docker-compose.yml` para expor porta nenhuma no host — o túnel passa
pela mesma conexão SSH que já existe.

## 5. Vários hosts operando ao mesmo tempo, não um de cada vez

Se você tem staging, produção-de-teste e uma máquina pessoal cadastrados,
a TUI (`unlarp tui`) não obriga a escolher "o host ativo" — a aba Syncs
mostra os itens de todos os hosts com sufixo `@host`, todos rodando
simultaneamente. Trocar de host (`unlarp use` ou `Enter` na sidebar) muda só
qual host recebe os *próximos* comandos do CLI; não pausa os outros.

## 6. Colar um screenshot direto no agente remoto

Você tem um erro visual, um mockup, um diff de layout — e quer que o Claude
Code que roda no container veja a imagem, sem fazer upload manual.

```bash
unlarp paste --session meu-projeto-wt-feat-x
```

Pega o clipboard do macOS (via `pngpaste`, ou fallback `osascript`), sobe o
arquivo para o servidor e cola o **caminho** no pane tmux certo — o agente lê
a imagem pelo caminho como faria com qualquer arquivo do disco.

## 7. Atualizar o agente do servidor sem matar as sessões

O `unlarp-agent` (o plano de controle dentro do container) recebeu uma
correção e você quer aplicá-la sem derrubar os tmux com agentes rodando há
horas.

```bash
unlarp agent install
```

Cross-compila, envia o binário novo, reinicia só o processo do agente — hot
swap. Um redeploy do container mataria as sessões tmux; isso não mata.

---

Tutorial comando-a-comando: `README.md`. Roadmap do daemon local (engines
sobrevivendo ao fechamento da TUI/CLI): `DAEMON.md`. Opções de acesso mobile
mais a fundo: `REMOTE.md`.
