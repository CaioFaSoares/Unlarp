#!/bin/bash

# ponytail: loop-restart é o supervisor; migrar p/ supervisord só se surgirem mais daemons
(while true; do
    echo "Iniciando dockerd interno..."
    dockerd >> /var/log/dockerd.log 2>&1
    echo "dockerd caiu (exit $?), reiniciando em 2s..."
    sleep 2
done) &

# Espera limitada — sshd sobe mesmo se o dockerd falhar (nunca perder o SSH)
for i in $(seq 30); do
    docker info >/dev/null 2>&1 && { echo "Docker interno operacional"; break; }
    sleep 1
done

echo "Iniciando servidor OpenSSH..."
exec /usr/sbin/sshd -D
