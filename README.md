# Avahi Router

Automatically publish mDNS hostnames for Docker containers using Avahi. Works alongside reverse proxies like Traefik to provide local domain names for your services.

## Overview

Avahi Router monitors Docker containers and automatically publishes mDNS (multicast DNS) hostnames via Avahi when containers start, and removes them when containers stop. This allows you to access your services using custom local domains (e.g., `http://myapp.local`) instead of IP addresses and ports.

**Note:** This tool only publishes DNS names. You still need a reverse proxy (like Traefik, Nginx Proxy Manager, or Caddy) to route incoming requests to the correct container ports.

## How It Works

1. **Label Detection**: Avahi Router watches for Docker containers with the `avahi.hostname` label
2. **mDNS Publishing**: When a labeled container starts, Avahi publishes the hostname pointing to the host IP
3. **Automatic Cleanup**: When the container stops, the mDNS entry is removed
4. **Reverse Proxy**: Your reverse proxy maps the hostname to the appropriate container port

## Quick Start

### 1. Start Avahi Router

```bash
# Set your host IP (optional - auto-detected if not set)
export HOST_IP=192.168.1.100

docker compose up -d
```

### 2. Add Labels to Your Containers

Add the `avahi.hostname` label to any container you want to expose:

```yaml
services:
  myapp:
    image: nginx
    labels:
      - "avahi.hostname=myapp.local"
```

### 3. Configure Your Reverse Proxy

#### Traefik Example

```yaml
services:
  traefik:
    image: traefik:v3.0
    command:
      - "--api.insecure=true"
      - "--providers.docker=true"
      - "--entrypoints.web.address=:80"
    ports:
      - "80:80"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock

  myapp:
    image: nginx
    labels:
      - "avahi.hostname=myapp.local"
      - "traefik.enable=true"
      - "traefik.http.routers.myapp.rule=Host(`myapp.local`)"
      - "traefik.http.routers.myapp.entrypoints=web"
```

Now you can access `http://myapp.local` from any device on your local network.

## Requirements

- Docker and Docker Compose
- Linux host (uses host networking mode)
- Avahi daemon running on the host
- A reverse proxy (Traefik, Nginx Proxy Manager, Caddy, etc.)

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `HOST_IP` | The IP address to advertise hostnames to | Auto-detected |
| `TRAEFIK_ENABLED` | Enable Traefik annotation support (see below) | `false` |

### Docker Labels

| Label | Description | Example |
|-------|-------------|---------|
| `avahi.hostname` | The mDNS hostname to publish | `myapp.local` |
| `traefik.enable` | Enable Traefik routing (required when using Traefik integration) | `true` |
| `traefik.http.routers.<name>.rule` | Traefik router rule with Host matcher | ``Host(`myapp.local`)`` |

## Traefik Integration

When `TRAEFIK_ENABLED=true` is set, Avahi Router can automatically extract hostnames from Traefik annotations, eliminating the need to duplicate the hostname in both `avahi.hostname` and `traefik.http.routers.*.rule` labels.

### How It Works

1. Enable Traefik support: `TRAEFIK_ENABLED=true`
2. Add `traefik.enable=true` to your container
3. Add a Traefik router rule with a `Host()` matcher
4. Avahi Router extracts the hostname from the rule and publishes it

**Note:** The `avahi.hostname` label still takes precedence if both are present.

### Example

```yaml
services:
  myapp:
    image: nginx
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.myapp.rule=Host(`myapp.local`)"
      - "traefik.http.routers.myapp.entrypoints=web"
```

With `TRAEFIK_ENABLED=true`, Avahi Router will automatically publish `myapp.local` via mDNS using the hostname from the Traefik rule.

### Supported Host Patterns

The following patterns are supported for extracting hostnames:

- ``Host(`hostname`)`` - Backtick quotes (most common)
- `Host("hostname")` - Double quotes
- ``Host(`host1`) && PathPrefix(`/api`)`` - Combined with other matchers (extracts first host)
- ``Host(`host1`) || Host(`host2`)`` - Multiple hosts (extracts first one)
