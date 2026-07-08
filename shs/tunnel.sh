#!/bin/bash
# Script para criar um túnel SSH (Port Forwarding) entre um serviço rodando no DinD e a sua máquina física.

if [ "$#" -lt 2 ]; then
  echo "Uso: $0 <porta_interna_dind> <porta_na_sua_maquina> [ip_do_host] [porta_ssh]"
  echo ""
  echo "Exemplo local (Postgres 5432):"
  echo "  $0 5432 5432"
  echo ""
  echo "Exemplo remoto (Supabase Studio 3000 -> 8080 no host remoto):"
  echo "  $0 3000 8080 192.168.1.100"
  echo ""
  echo "Exemplo com porta SSH customizada:"
  echo "  $0 8080 8080 localhost 2222"
  exit 1
fi

DIND_PORT=$1
LOCAL_PORT=$2
HOST_IP=${3:-"localhost"}
SSH_PORT=${4:-"2222"}

echo "=== Estabelecendo túnel SSH ==="
echo "Direcionando: física(localhost:$LOCAL_PORT) -> workspace($HOST_IP:$DIND_PORT) via SSH na porta $SSH_PORT"
echo "Pressione Ctrl+C para encerrar o túnel."
echo ""

ssh -N -L "$LOCAL_PORT:127.0.0.1:$DIND_PORT" "root@$HOST_IP" -p "$SSH_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null
