FROM debian:trixie-slim

# Evita interações manuais durante a instalação de pacotes
ENV DEBIAN_FRONTEND=noninteractive

# 1. Instalar utilitários essenciais do sistema
# xz-utils é obrigatório para o instalador do Nix funcionar
RUN apt-get update && apt-get install -y \
    apt-transport-https \
    ca-certificates \
    curl \
    gnupg \
    lsb-release \
    git \
    tmux \
    openssh-server \
    procps \
    sudo \
    build-essential \
    xz-utils \
    && rm -rf /var/lib/apt/lists/*

# 2. Instalar Docker (DinD)
# Como o Debian 13 (Trixie) ainda está em testing, o script oficial da Docker 
# é a forma mais segura de mapear e instalar a versão compatível correta.
RUN curl -fsSL https://get.docker.com -o get-docker.sh \
    && sh get-docker.sh \
    && rm get-docker.sh

# 3. Instalar Node.js e Claude Code globalmente
# Deixamos o Claude Code no nível do host do container pois ele é sua 
# "ferramenta de infraestrutura", e não uma dependência do projeto.
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y nodejs \
    && npm install -g @anthropic-ai/claude-code \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

# 4. Instalar o Gerenciador de Pacotes Nix (Single User Mode)
RUN curl -L https://nixos.org/nix/install | bash -s -- --no-daemon

# Adicionar Nix ao PATH global do Dockerfile
ENV PATH="/root/.nix-profile/bin:/nix/var/nix/profiles/default/bin:$PATH"

# 5. Habilitar Flakes e Nix Command nativamente
RUN mkdir -p /etc/nix && \
    echo "experimental-features = nix-command flakes" >> /etc/nix/nix.conf

# Garantir que o Nix seja carregado sempre que você logar via SSH com bash
RUN echo "if [ -e /root/.nix-profile/etc/profile.d/nix.sh ]; then . /root/.nix-profile/etc/profile.d/nix.sh; fi" >> /root/.bashrc

# 6. Configurar o Servidor SSH
RUN mkdir /var/run/sshd \
    && mkdir -p /root/.ssh && chmod 700 /root/.ssh \
    && sed -i 's/#PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config \
    && sed -i 's/#PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config

WORKDIR /workspace

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 22

ENTRYPOINT ["/entrypoint.sh"]