#!/bin/bash
# Script para conectar via SSH no container local (porta 2222)

echo "=== Conectando ao workspace local ==="
ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null root@localhost -p 2222
