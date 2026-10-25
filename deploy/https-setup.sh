#!/usr/bin/env bash
# v1.7.0 HTTPS termination for Tencent IM webhook public delivery.
# nginx 443 -> im2a-gateway(:80). After DNS A record points at this host,
# run:  sudo bash deploy/https-setup.sh im2a.example.com
set -euo pipefail
DOMAIN="${1:-}"
if [ -z "$DOMAIN" ]; then
  echo "usage: $0 <domain>   (DNS A record must point to this server)" >&2
  exit 2
fi
echo "[1/5] install nginx + certbot"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq nginx certbot python3-certbot-nginx
echo "[2/5] write vhost (port 80 for ACME challenge)"
cat > /etc/nginx/sites-available/im2a <<NGINX
server {
    listen 80;
    server_name $DOMAIN;
    client_max_body_size 4m;
    location / {
        proxy_pass http://127.0.0.1:80;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }
}
NGINX
rm -f /etc/nginx/sites-enabled/default /etc/nginx/sites-enabled/im2a
ln -sf /etc/nginx/sites-available/im2a /etc/nginx/sites-enabled/im2a
nginx -t
systemctl reload nginx
echo "[3/5] obtain trusted certificate (Let's Encrypt)"
certbot --nginx -d "$DOMAIN" --non-interactive --agree-tos -m "admin@${DOMAIN#*.}" --redirect
systemctl reload nginx
echo "[4/5] reload done"
echo "[5/5] Tencent IM callback URL to fill in console:"
SECRET=$(systemctl show im2a-gateway -p Environment --value | tr ' ' '\n' | grep TIM_WEBHOOK_SECRET | head -1 | sed 's/^[^=]*=//')
echo "    https://$DOMAIN/v1/im/webhook?key=$SECRET"
