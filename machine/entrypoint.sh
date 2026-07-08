#!/bin/bash

# Garante a criação do diretório .ssh
mkdir -p /root/.ssh
chmod 700 /root/.ssh

# Garante permissões corretas no diretório home do root (evita erros do sshd com volumes montados)
chown -R root:root /root
chmod 700 /root
chmod 700 /root/.ssh 2>/dev/null
if [ -f /root/.ssh/authorized_keys ]; then
    chmod 600 /root/.ssh/authorized_keys
fi

echo "Iniciando Docker Daemon interno (DinD)..."
# Inicia o docker daemon em background
dockerd > /var/log/dockerd.log 2>&1 &

# Aguarda até o Docker estar online
while ! docker info >/dev/null 2>&1; do
    echo "Aguardando dockerd..."
    sleep 1
done
echo "Docker Daemon operacional!"

echo "Iniciando servidor OpenSSH..."
# Executa o SSH em foreground
exec /usr/sbin/sshd -D
