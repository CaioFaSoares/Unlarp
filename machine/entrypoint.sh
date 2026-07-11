#!/bin/bash

# ponytail: loop-restart é o supervisor; migrar p/ supervisord só se surgirem mais daemons
(while true; do
    echo "Iniciando dockerd interno..."
    dockerd >> /var/log/dockerd.log 2>&1
    echo "dockerd caiu (exit $?), reiniciando em 2s..."
    sleep 2
done) &

(while true; do
    echo "Iniciando unlarp-agent..."
    /usr/local/bin/unlarp-agent >> /var/log/unlarp-agent.log 2>&1
    echo "unlarp-agent caiu (exit $?), reiniciando em 2s..."
    sleep 2
done) &

# Garante mouse scroll + scrollback amplo no tmux, mesmo com o volume /root
# sombreando o que a imagem gravou (o volume nomeado persiste entre builds).
grep -qs 'mouse on' /root/.tmux.conf 2>/dev/null || cat >> /root/.tmux.conf <<'EOF'
set -g mouse on
set -g history-limit 50000
EOF

# Espera limitada — sshd sobe mesmo se o dockerd falhar (nunca perder o SSH)
for i in $(seq 30); do
    docker info >/dev/null 2>&1 && { echo "Docker interno operacional"; break; }
    sleep 1
done

echo "Iniciando servidor OpenSSH..."
exec /usr/sbin/sshd -D
