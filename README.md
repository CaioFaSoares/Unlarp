# Virtual Dev Workspace (Debian 13 + Nix + DinD)

Este repositório contém as definições para subir e rodar um workspace virtual de desenvolvimento focado em isolamento completo, reprodutibilidade com Nix Flakes, ferramentas modernas de IA (Claude Code) e suporte nativo a Docker-in-Docker (DinD).

---

## 🚀 Arquitetura e Recursos

- **Debian 13 (Trixie-slim)**: Ambiente Linux moderno e leve.
- **Nix (Single-User Mode)**: Gerenciador de pacotes ativado nativamente com suporte a **Flakes** e **Nix Commands**.
- **Docker-in-Docker (DinD)**: Possibilidade de rodar containers (como bancos de dados locais de teste) dentro do seu workspace isolado.
- **Claude Code**: Pré-instalado globalmente no container como seu assistente de terminal.
- **SSH Pronto**: Acesso remoto rápido (porta `2222` do host) via par de chaves SSH (senha desativada por segurança).
- **Persistência Brutal**: Volumes nomeados montados para garantir que seu código, dependências Nix, chaves SSH e cache do Docker não sejam perdidos ao recriar o container.

---

## 🛠️ Pré-requisitos

1. **Docker e Docker Compose** instalados na sua máquina local.
2. Uma chave SSH pública configurada na sua máquina local em `~/.ssh/` (ex: `id_rsa.pub` ou `id_ed25519.pub`). Se não tiver, gere uma com:
   ```bash
   ssh-keygen -t ed25519
   ```

---

## 💻 Como Usar Localmente (Passo a Passo)

### 1. Iniciar o Workspace
No diretório do projeto, execute o Docker Compose em segundo plano:
```bash
docker compose up -d
```

### 2. Configurar a Chave SSH
Para conseguir se conectar sem senha, rode o script utilitário de configuração de chave local:
```bash
./shs/setup-local-ssh.sh
```
*Esse script detectará sua chave pública local, copiará para o container e ajustará as permissões corretas.*

### 3. Conectar ao Workspace via SSH
Use o script de conexão rápida:
```bash
./shs/connect-local.sh
```
Ou conecte-se manualmente usando o comando:
```bash
ssh root@localhost -p 2222
```

---

## 🌐 Deploy no Coolify (Servidor Remoto)

### 1. Criar Aplicação no Coolify
1. No painel do Coolify, adicione um novo recurso do tipo **Docker Compose**.
2. Aponte para este repositório Git ou cole o conteúdo do arquivo `docker-compose.yml`.
3. Certifique-se de que os volumes estão mapeados como **Named Volumes** (como definido no nosso compose padrão):
   - `workspace-data` -> `/workspace`
   - `workspace-nix-store` -> `/nix`
   - `workspace-ssh` -> `/root/.ssh`
   - `workspace-docker` -> `/var/lib/docker`
4. Deploy a aplicação.

### 2. Configurar Chave SSH Remota
Após o Coolify iniciar o container, você precisará liberar sua chave física no servidor.
1. Na sua máquina local, rode o gerador de comandos:
   ```bash
   ./shs/setup-remote-ssh.sh
   ```
2. Copie o comando gerado (que contém a sua chave pública).
3. Cole e execute no terminal do seu servidor remoto.
4. Conecte-se remotamente:
   ```bash
   ssh root@IP_DO_SEU_SERVIDOR -p 2222
   ```

---

## 📂 Organização dos Volumes (Persistência Brutal)

Para evitar perda de dados durante atualizações do Coolify ou restarts de container:
- `/workspace`: Onde ficam os seus códigos de projetos e repositórios.
- `/nix`: Pasta onde ficam o Nix store e os pacotes instalados via Flakes.
- `/root/.ssh`: Onde ficam as chaves públicas autorizadas a acessar o terminal.
- `/var/lib/docker`: Local de cache e dados do Docker interno (DinD).

---

## ⚙️ Fluxo de Trabalho Diário

Uma vez conectado via SSH no workspace:

1. **Criar ou abrir um projeto na pasta `/workspace`**:
   ```bash
   cd /workspace
   mkdir meu-projeto && cd meu-projeto
   ```

2. **Criar seu ambiente com Nix Flakes**:
   Crie um arquivo `flake.nix` definindo as ferramentas necessárias (Python, Go, Node, Rust, etc.). Exemplo simples para Node.js:
   ```nix
   {
     description = "Node.js environment";
     inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
     outputs = { self, nixpkgs }: {
       devShells.x86_64-linux.default = let
         pkgs = nixpkgs.legacyPackages.x86_64-linux;
       in pkgs.mkShell {
         buildInputs = [ pkgs.nodejs_20 ];
       };
     };
   }
   ```

3. **Ativar o Ambiente**:
   ```bash
   nix develop
   ```
   *Pronto! Você estará dentro de um shell isolado com o Node.js disponível sem ter instalado nada no Debian.*

4. **Rodar dependências em Docker (DinD)**:
   Se seu projeto precisa de um banco PostgreSQL ou Redis, crie um `docker-compose.yml` para ele e suba o serviço diretamente de dentro do seu workspace:
   ```bash
   docker compose up -d postgres
   ```

5. **Utilizar o Claude Code**:
   Execute o Claude Code diretamente no seu workspace para auxiliar na escrita de código ou na infraestrutura:
   ```bash
   claude
   ```
