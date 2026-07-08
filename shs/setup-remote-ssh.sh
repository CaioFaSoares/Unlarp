#!/bin/bash
# Script para gerar comando de autorização para o servidor remoto

# Detecta chave SSH pública local
SSH_KEY=""
for key in ~/.ssh/id_rsa.pub ~/.ssh/id_ed25519.pub ~/.ssh/id_ecdsa.pub; do
  if [ -f "$key" ]; then
    SSH_KEY="$key"
    break
  fi
done

if [ -z "$SSH_KEY" ]; then
  echo "Erro: Nenhuma chave pública SSH encontrada em ~/.ssh."
  exit 1
fi

KEY_CONTENT=$(cat "$SSH_KEY")

echo "=== Instruções para Configuração Remota (Coolify) ==="
echo "Execute o seguinte comando no terminal do seu servidor remoto onde está rodando o Coolify:"
echo ""
echo "docker exec -i workspace_machine sh -c \"mkdir -p /root/.ssh && chmod 700 /root/.ssh && echo '$KEY_CONTENT' >> /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys && chown -R root:root /root/.ssh\""
echo ""
echo "Após executar, você poderá conectar remotamente com:"
echo "ssh root@IP_DO_SEU_SERVIDOR -p 2222"
