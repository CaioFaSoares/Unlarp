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

# .claude/settings.local.json do usuário aponta ANTHROPIC_BASE_URL pro headroom
# (127.0.0.1:8787) e é sincronizado pro container — sem o proxy sempre de pé,
# o `claude` puro (sem passar pelo wrapper) recebe ConnectionRefused.
if [ -x /root/.local/bin/headroom ]; then
    (while true; do
        echo "Iniciando headroom proxy (porta 8787)..."
        /root/.local/bin/headroom proxy --port 8787 >> /var/log/headroom.log 2>&1
        echo "headroom proxy caiu (exit $?), reiniciando em 2s..."
        sleep 2
    done) &
else
    echo "headroom ausente em /root/.local/bin — pulando supervisor do proxy"
fi

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
