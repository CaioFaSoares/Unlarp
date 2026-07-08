# Virtual Dev Workspace (Debian 13 + Nix + DinD)

Este repositório contém as definições para subir e rodar um workspace virtual de desenvolvimento focado em isolamento completo, reprodutibilidade com Nix Flakes, ferramentas modernas de IA e suporte nativo a Docker-in-Docker (DinD).

A interação com o workspace local ou remoto é gerida através do CLI **Unlarp** em Go, que substitui scripts legados de conexão, adiciona suporte a sincronização bidirecional em tempo real (sem necessidade de SSHFS ou FUSE no Mac) e oferece uma TUI interativa.

---

## 🛠️ O CLI Unlarp (Go + Cobra + Viper)

O CLI Unlarp (`unlarp`) é a sua central de controle. Ele lê a configuração do arquivo `~/.unlarp.yaml` e persiste o estado da sessão ativa em `~/.unlarp/state.json`.

### Como Compilar e Instalar

No diretório raiz deste projeto:

```bash
# Entrar na pasta do CLI
cd cli

# Compilar o binário
go build -o unlarp .

# Instalar globalmente no sistema
mv unlarp /usr/local/bin/
```

---

## 💻 Comandos e Fluxo de Trabalho (Tutorial)

### 1. Configurar Conexões (`unlarp config`)

Você pode adicionar conexões manualmente ou importando uma entrada existente do seu `~/.ssh/config` (modo read-only, não modifica o seu arquivo original):

```bash
# Importar automaticamente do ~/.ssh/config
unlarp config add meu-servidor --from-ssh-config meu-alias --workspace /workspace

# Adicionar manualmente
unlarp config add local --host localhost --port 2222 --user root --workspace /workspace --container workspace_machine

# Listar todos os perfis configurados
unlarp config list

# Editar um host (abre no $EDITOR) ou via flag inline
unlarp config edit local --port 2223
```

### 2. Injetar Chave SSH (`unlarp setup`)

Para configurar conexões sem senha de forma segura:

```bash
# Auto-detecta chaves locais em ~/.ssh e injeta no authorized_keys do host
unlarp setup meu-servidor
```

### 3. Alternar entre Sessões (`unlarp use`)

Você pode trocar o host padrão para os comandos subsequentes:

```bash
unlarp use meu-servidor
```

### 4. SSH e Execução Remota (`unlarp connect` / `unlarp exec`)

Substitui os scripts de conexão antigos, com suporte a sessões virtuais persistentes via Tmux:

```bash
# Conectar à sessão ativa em um terminal interativo completo (com PTY)
unlarp connect

# Conectar abrindo uma sessão persistente do Tmux (com auto-criação/auto-anexação)
unlarp connect --tmux

# Abrir uma sessão persistente com nome customizado (ex: claude-dev)
unlarp connect --tmux --tmux-session claude-dev

# Executar um comando remoto e retornar a saída
unlarp exec -- ls -la /workspace
unlarp exec -- nix develop --command "go version"
```

### 5. Sincronização Bidirecional em Tempo Real (`unlarp sync`)

A sincronização de arquivos do Unlarp é baseada em um algoritmo **Three-way Reconciliation** (Reconciliação de 3 vias) usando um histórico de estado local. 
**Não requer SSHFS nem extensões de kernel (FUSE) no macOS.**

Você edita os arquivos localmente no seu Mac na sua IDE de preferência e eles são propagados para o container via SFTP em milissegundos. Alterações remotas (geradas por builds ou instalações) são baixadas de volta.

```bash
# Inicia a sincronização do projeto atual com o workspace
unlarp sync start --local-dir . --remote-dir /workspace/meu-projeto

# Verifica o status das sessões de sincronização
unlarp sync status

# Parar uma sessão de sincronização
unlarp sync stop s-abc123
```

Você pode criar um arquivo `.unlarpignore` na raiz do seu projeto local seguindo a sintaxe do `.gitignore` para pular arquivos e pastas (ex: `node_modules/`, `.git/`, `dist/`).

### 6. Túneis SSH / Port Forwarding (`unlarp tunnel`)

Para acessar portas internas rodando no Docker-in-Docker (DinD) remoto de volta no seu Mac (ex: um banco Postgres na 5432):

```bash
# Encaminha a porta remota 5432 para localhost:5432 no Mac (foreground por padrão)
unlarp tunnel 5432

# Encaminha portas diferentes (remota 3000 -> local 8080)
unlarp tunnel 3000:8080

# Múltiplas portas de uma vez
unlarp tunnel 5432,3000:8080,6379

# Rodar o túnel em background
unlarp tunnel 5432 -b

# Listar e parar túneis ativos
unlarp tunnel list
unlarp tunnel stop t-xyz123
```

---

## 📺 Dashboard Interativa (TUI)

A dashboard interativa centraliza todas as funcionalidades em uma única interface visual de terminal desenvolvida com a Charm Stack (`bubbletea`, `lipgloss` e `bubbles`).

```bash
unlarp tui
```

### 🚀 Onboarding Wizard (Primeiro Acesso)
Se você não tiver nenhum host configurado no arquivo `~/.unlarp.yaml`, a TUI abrirá automaticamente um **Assistente de Onboarding**. Ele guiará você passo a passo para cadastrar o IP, porta, usuário, caminho remoto do workspace e injetará sua chave pública SSH em segundo plano com feedback visual (spinner), salvando a configuração final para você.

### 🔄 Persistência de Sessões Tmux (Claude Code / Agentes)
A aba **Dashboard** da TUI mostra a lista de sessões virtuais Tmux ativas no servidor remoto, indicando se estão conectadas (`ATTACHED`) ou em background (`DETACHED`).
* **Enquadramento Unlarp**: A barra inferior do Tmux remoto é customizada nas cores roxa e ciano com a identificação do Unlarp e um lembrete visual de detach.
* **Desconexão Segura (`Ctrl+G`)**: Você pode se desanexar de qualquer sessão Tmux a qualquer momento pressionando **`Ctrl+G`** (sem prefixo). Isso devolve você à TUI imediatamente e mantém seus processos (como agentes de código) rodando no container remoto.
* **Redimensionamento (`SIGWINCH`)**: O PTY local propaga alterações de tamanho de janela dinamicamente para o Tmux remoto, eliminando quebras de layout.

### Controles da TUI:
- **`Tab`**: Alterna o foco entre a barra lateral de sessões (hosts) e o painel de abas principal.
- **`Setas Cima/Baixo` (ou `j/k`)**:
  - Na barra lateral: Seleciona um host diferente.
  - No painel principal: Navega pelas tabelas de **Syncs**, **Túneis** e lista de **Sessões Tmux** (na aba Dashboard).
- **`Enter`**: Define o host selecionado como a sessão ativa no CLI.
- **`a`**: Adiciona um novo host remotamente chamando o Onboarding Wizard.
- **`x` ou `d`**: Deleta o host selecionado na barra lateral, ou encerra o item selecionado (sync, túnel ou sessão Tmux remota) na aba ativa do painel principal.
- **`Setas Esquerda/Direita`**: Navega entre as abas do painel principal (Dashboard ↔ Syncs ↔ Túneis ↔ Logs).
- **`c`**: Conecta/anexa (attach) na sessão Tmux selecionada no Dashboard.
- **`n`**: Abre o prompt na aba Dashboard para criar uma nova sessão Tmux remota nomeada.
- **`s`**: Inicia uma nova sessão de sincronização bidirecional em tempo real a partir de um prompt na aba de Syncs. (Ex: `local_dir:remote_dir` ou apenas `remote_dir`).
- **`t`**: Cria um novo túnel SSH port forwarding a partir de um prompt na aba de Túneis. (Ex: `5432` ou `3000:8080`).
- **`x`**: Encerra e remove o item selecionado (sync, túnel ou sessão Tmux remota) na aba ativa do painel principal.
- **`q` ou `Ctrl+C`**: Sai da TUI de forma limpa, finalizando todas as conexões ativas.

---

## 🌐 Deploy no Coolify (Servidor Remoto)

### 1. Criar Aplicação no Coolify
1. No painel do Coolify, adicione um novo recurso do tipo **Docker Compose**.
2. Cole o conteúdo do arquivo `docker-compose.yml` deste repositório.
3. Configure os Named Volumes:
   - `workspace-data` -> `/workspace`
   - `workspace-nix-store` -> `/nix`
   - `workspace-home` -> `/root` (persiste chaves SSH, preferências de terminal, login de agentes e histórico)
   - `workspace-docker` -> `/var/lib/docker`
4. Deploy a aplicação.

O container inicializará o servidor SSH de forma limpa. Em seguida, basta abrir a TUI local (`./unlarp tui`), apertar `a` na barra lateral e passar as credenciais do seu host. O assistente de Onboarding do Unlarp detectará se o container está sem a sua chave SSH local e usará automaticamente a sua conexão com o host VPS (porta 22) para injetar a chave pública dentro do container via `docker exec`, sem requerer nenhuma configuração de variável de ambiente ou senhas.
