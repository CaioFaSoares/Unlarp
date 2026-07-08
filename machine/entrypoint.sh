#!/bin/bash

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
