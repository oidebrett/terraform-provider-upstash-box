# Standalone OpenClaw on a serverless Upstash Box, reachable at a public URL.
#   export UPSTASH_BOX_API_KEY=...   (Box key, not the Redis developer key)
#   terraform init && terraform apply -var openclaw_token=$(openssl rand -hex 24)

terraform {
  required_providers {
    upstashbox = {
      source  = "registry.example.com/mattercoder/upstash-box"
      version = "0.1.0"
    }
  }
}

provider "upstashbox" {}

variable "openclaw_token" {
  type      = string
  sensitive = true
}

variable "keep_alive" {
  description = "false = serverless (pauses when idle); true = always on (paid plan)"
  type        = bool
  default     = false
}

resource "upstashbox_box" "agent" {
  name       = "openclaw-demo"
  size       = "small"
  runtime    = "node"
  keep_alive = var.keep_alive
  # Runs on every container start, so the gateway comes back after a resume.
  init_command = "/home/boxuser/start-openclaw.sh"
}

resource "upstashbox_public_url" "dashboard" {
  box_id = upstashbox_box.agent.id
  port   = 18789
  # No Upstash auth: OpenClaw checks its own token (/#token=...).
}

resource "upstashbox_exec" "install" {
  box_id = upstashbox_box.agent.id
  script = <<-EOT
    set -eu
    sudo -n npm install -g openclaw
    # The Upstash proxy connects from the box's docker bridge (172.19.0.1) and
    # appends to X-Forwarded-For, so trust the bridge and allow the public origin.
    openclaw config set gateway.trustedProxies '["172.16.0.0/12"]' --strict-json
    openclaw config set gateway.controlUi.allowedOrigins '["${upstashbox_public_url.dashboard.url}"]' --strict-json
    openclaw config set gateway.auth.mode token
    openclaw config set gateway.auth.token '${var.openclaw_token}'
    cat > /home/boxuser/start-openclaw.sh <<'EOS'
    #!/bin/sh
    nohup openclaw gateway run --allow-unconfigured --bind lan --port 18789 > /tmp/openclaw-gateway.log 2>&1 &
    EOS
    chmod +x /home/boxuser/start-openclaw.sh
    /home/boxuser/start-openclaw.sh
  EOT
}

output "dashboard_url" {
  value     = "${upstashbox_public_url.dashboard.url}/#token=${var.openclaw_token}"
  sensitive = true
}
