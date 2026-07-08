#!/bin/bash
# Script para injetar chaves públicas locais no container workspace_machine

CONTAINER_NAME="workspace_machine"

echo "=== Configurando chaves SSH no container local ==="

# Verifica se o container está rodando
if ! docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
  echo "Erro: O container '${CONTAINER_NAME}' não está rodando."
  echo "Por favor, suba o container usando: docker compose up -d"
  exit 1
fi

# Detecta chave SSH pública local
SSH_KEY=""
for key in ~/.ssh/id_rsa.pub ~/.ssh/id_ed25519.pub ~/.ssh/id_ecdsa.pub; do
  if [ -f "$key" ]; then
    SSH_KEY="$key"
    break
  fi
done

if [ -z "$SSH_KEY" ]; then
  echo "Erro: Nenhuma chave pública SSH encontrada em ~/.ssh (procurou id_rsa.pub, id_ed25519.pub, id_ecdsa.pub)."
  echo "Crie uma chave rodando: ssh-keygen -t ed25519"
  exit 1
fi

echo "Usando chave pública: $SSH_KEY"

# Cria pasta .ssh no container
docker exec -it "$CONTAINER_NAME" mkdir -p /root/.ssh

# Copia a chave
docker cp "$SSH_KEY" "$CONTAINER_NAME":/root/.ssh/authorized_keys

# Ajusta as permissões
docker exec -it "$CONTAINER_NAME" chmod 700 /root/.ssh
docker exec -it "$CONTAINER_NAME" chmod 600 /root/.ssh/authorized_keys
docker exec -it "$CONTAINER_NAME" chown -R root:root /root/.ssh

echo "Sucesso! Chave SSH copiada e permissões configuradas no container."
