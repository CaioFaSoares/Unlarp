# REMOTE.md — Acesso pelo telefone aos terminais do servidor

> Status: **avaliação** — nada aqui está implementado. O objetivo é acompanhar
> e comandar as sessões tmux do servidor (agentes Claude Code) a partir do
> celular: ver o que o agente está fazendo, responder uma permissão, disparar
> um comando.

## Contexto

O servidor é um container com sshd (porta 2222), tmux e os agentes rodando em
sessões persistentes. Tudo que o Mac faz via `unlarp connect --tmux` é, no fim,
`ssh` + `tmux attach` — e é exatamente isso que o telefone também consegue
fazer. As opções abaixo estão em ordem de esforço.

## Opção A — App de SSH no celular + `tmux attach` (recomendada, custo zero)

**Como**: instalar um cliente SSH mobile — [Termius](https://termius.com)
(iOS/Android) ou [Blink Shell](https://blink.sh) (iOS) — cadastrar o servidor
(mesma chave SSH, porta 2222) e rodar `tmux attach -t <sessão>`.

- **Prós**: funciona hoje, sem uma linha de código; é a MESMA sessão que o Mac
  vê (você assume o controle de onde parou); Termius sincroniza hosts entre
  aparelhos; teclado com Ctrl/Esc/setas embutido.
- **Contras**: exige o servidor alcançável do celular (IP público ou VPN — ver
  Opção C); UX de terminal em tela pequena; chave SSH armazenada no aparelho.
- **Dica**: `tmux attach` com a sessão do agente + `mouse on` (já configurado
  no entrypoint) dá scroll por toque. Para só acompanhar sem interferir:
  `tmux attach -r` (read-only).

## Opção B — Terminal no browser: ttyd servindo tmux (incremento seguinte)

**Como**: instalar [ttyd](https://github.com/tsl0922/ttyd) na imagem e servir
o tmux via web: `ttyd -p 7681 -c user:senha tmux attach -t unlarp`. Acessar
`http://<servidor>:7681` de qualquer browser (celular incluído).

- **Prós**: zero app no celular; funciona em qualquer dispositivo; dá para
  servir uma sessão específica por porta/rota.
- **Contras**: mais um daemon no entrypoint; auth básica + TLS por sua conta —
  **não expor na internet sem estar atrás de VPN (Opção C) ou reverse proxy
  com TLS**; ttyd entra na imagem Docker (+1 pacote).
- **Esforço**: ~10 linhas (Dockerfile + entrypoint + porta no compose). Um
  `unlarp tunnel 7681` já o expõe no Mac sem abrir porta pública, mas para o
  telefone o par natural é ttyd + Tailscale.

## Opção C — Tailscale no container (transporte para A e B)

Não é uma alternativa às opções acima — é o **transporte** que as viabiliza sem
expor porta pública nenhuma.

**Como**: `tailscaled` no entrypoint (estado em volume para sobreviver a
recreates) + `tailscale up --authkey=...`. O celular entra na mesma tailnet e
alcança o servidor pelo IP privado (SSH da Opção A, browser da Opção B).

- **Prós**: zero porta exposta; ACLs por dispositivo; MagicDNS
  (`ssh root@workspace`); o app mobile do Tailscale é sólido.
- **Contras**: estado do tailscaled precisa de volume próprio; login/authkey a
  gerenciar; dependência de serviço externo (há o Headscale self-hosted, mais
  esforço).

## Opção D — Estender o unlarp-agent com endpoints tmux + web UI mínima

**Como**: o `unlarp-agent` (HTTP sobre unix socket) ganharia endpoints
`GET /v1/tmux/{sessão}/capture` (capture-pane) e `POST /v1/tmux/{sessão}/keys`
(send-keys, com allowlist como no GitOp), mais uma página web mobile-friendly
mostrando o pane e um campo de input — exposta via ttyd-like ou porta própria
atrás do Tailscale.

- **Prós**: UX sob medida (botões "aprovar/negar" para permissões do Claude,
  status dos agentes como na TUI); read-only controlado de verdade.
- **Contras**: é escrever um front + auth própria + API nova — **de longe o
  maior esforço**, para resolver o que A+C já resolvem.
- **Veredito**: registrado e **não recomendado por ora** (YAGNI). Só faz
  sentido se A/B mostrarem um limite real de uso no dia a dia.

## Recomendação

1. **Agora**: Opção A (Termius/Blink) com Opção C (Tailscale) como transporte —
   custo de código zero; o único trabalho é subir o tailscaled no container.
2. **Depois, se quiser browser**: Opção B (ttyd) atrás da mesma tailnet.
3. **D fica arquivada** até existir demanda concreta que A/B não cubram.

## Checklist quando formos implementar (A + C)

- [ ] `tailscale`/`tailscaled` na imagem (`machine/Dockerfile`) + volume de estado
- [ ] `tailscaled` no supervisor do `machine/entrypoint.sh` (mesmo padrão do dockerd/agent)
- [ ] `TS_AUTHKEY` via env do compose (Coolify secret)
- [ ] Documentar no README: cadastrar host no Termius com a chave existente
- [ ] Opcional: sessão tmux "cockpit" com `tmux new-session -s phone \; set -g status off` para tela pequena
