#!/bin/bash
# OmniCatalog Installer
# Interactive installer for OmniCatalog systemd service

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() { echo -e "${BLUE}[INFO]${NC} $*"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }

# Default values
DEFAULT_CONFIG_DIR="$HOME/.config/omnicatalog"
DEFAULT_DATA_DIR="$HOME/.local/share/omnicatalog"
DEFAULT_LOG_DIR="/var/log"
OMNICATALOG_BINARY="omnicatalog"
SERVICE_NAME="omnicatalog"

# Check if running as root
check_root() {
    if [[ $EUID -ne 0 ]]; then
        log_error "This script must be run as root (use sudo)"
        exit 1
    fi
}

# Check if omnicatalog binary exists
check_binary() {
    if [[ ! -f "./build/$OMNICATALOG_BINARY" ]]; then
        log_error "Binary not found at ./build/$OMNICATALOG_BINARY"
        log_info "Run 'make build-prod' first"
        exit 1
    fi
}

# Prompt for configuration
prompt_config() {
    echo
    log_info "=== OmniCatalog Configuration ==="
    echo

    # OmniRoute URL
    read -p "OmniRoute base URL [http://localhost:20128]: " OMNIROUTE_URL
    OMNIROUTE_URL=${OMNIROUTE_URL:-http://localhost:20128}

    # API Key
    echo
    log_info "OmniRoute API Key (get from OmniRoute dashboard)"
    log_warn "Leave empty to configure later via {file:...} or environment variable"
    read -p "API Key [optional]: " API_KEY

    # Schedule
    echo
    echo "Schedule options:"
    echo "  1) Daily at 3 AM (default)"
    echo "  2) Weekly on Sunday at 3 AM"
    echo "  3) Custom cron expression"
    read -p "Schedule [1]: " SCHEDULE_CHOICE
    SCHEDULE_CHOICE=${SCHEDULE_CHOICE:-1}

    case $SCHEDULE_CHOICE in
        1) SCHEDULE="daily"; CUSTOM_CRON="" ;;
        2) SCHEDULE="weekly"; CUSTOM_CRON="" ;;
        3)
            SCHEDULE="custom"
            read -p "Cron expression (e.g., '0 3 * * 1,3,5'): " CUSTOM_CRON
            ;;
        *) SCHEDULE="daily"; CUSTOM_CRON="" ;;
    esac

    # Config directory
    echo
    read -p "Config directory [$DEFAULT_CONFIG_DIR]: " CONFIG_DIR
    CONFIG_DIR=${CONFIG_DIR:-$DEFAULT_CONFIG_DIR}

    # Data directory
    read -p "Data directory [$DEFAULT_DATA_DIR]: " DATA_DIR
    DATA_DIR=${DATA_DIR:-$DEFAULT_DATA_DIR}

    # Log file path
    read -p "Log file path [/var/log/omnicatalog.log]: " LOG_FILE
    LOG_FILE=${LOG_FILE:-/var/log/omnicatalog.log}

    # User to run service
    echo
    read -p "System user to run service [$(logname)]: " SERVICE_USER
    SERVICE_USER=${SERVICE_USER:-$(logname)}

    # Confirm
    echo
    log_info "=== Configuration Summary ==="
    echo "  OmniRoute URL: $OMNIROUTE_URL"
    echo "  API Key: ${API_KEY:-[not set, will use env/file]}"
    echo "  Schedule: $SCHEDULE${CUSTOM_CRON:+ (custom: $CUSTOM_CRON)}"
    echo "  Config dir: $CONFIG_DIR"
    echo "  Data dir: $DATA_DIR"
    echo "  Log file: $LOG_FILE"
    echo "  Service user: $SERVICE_USER"
    echo
    read -p "Proceed with installation? [y/N]: " CONFIRM
    CONFIRM=${CONFIRM:-N}
    if [[ ! $CONFIRM =~ ^[Yy]$ ]]; then
        log_info "Installation cancelled"
        exit 0
    fi
}

# Create directories
create_directories() {
    log_info "Creating directories..."
    mkdir -p "$CONFIG_DIR"
    mkdir -p "$DATA_DIR"
    mkdir -p "$(dirname "$LOG_FILE")"
    chown -R "$SERVICE_USER:$SERVICE_USER" "$CONFIG_DIR" "$DATA_DIR" "$(dirname "$LOG_FILE")"
}

# Generate config.yaml
generate_config() {
    log_info "Generating config.yaml..."
    cat > "$CONFIG_DIR/config.yaml" <<EOF
omniroute:
  base_url: "$OMNIROUTE_URL"
  api_key: "${API_KEY:-\{env:OMNIROUTE_API_KEY\}}"
  timeout: 30s

opencode:
  config_path: "~/.config/opencode/opencode.json"
  backup_before_update: true

validation:
  api_tool_call_timeout: 30s
  opencode_test_timeout: 60s
  max_concurrent_tests: 3
  revalidate_after_hours: 24

speed_test:
  warmup_requests: 1
  ttft_samples: 3
  throughput_tokens: 500
  min_ttft_ms: 3000
  min_tps: 20
  tool_call_samples: 2

context_validation:
  enabled: true
  test_tokens: 100000
  min_context_orchestrate: 1000000
  min_context_code: 200000
  min_context_review: 1000000
  min_context_contracts: 200000
  min_context_database: 200000
  min_context_docs: 1000000

free_validation:
  enabled: true
  window_per_provider_sec: 30
  max_concurrent_providers: 5
  test_method: "billing_check"
  cache_ttl_hours: 24

scheduler:
  enabled: true
  schedule: "$SCHEDULE"
  custom_cron: "$CUSTOM_CRON"
  run_on_startup: true

database:
  path: "$DATA_DIR/catalog.db"
  migration_dir: "./migrations"

logging:
  level: "info"
  format: "json"
  output: "both"
  file_path: "$LOG_FILE"
EOF
    chown "$SERVICE_USER:$SERVICE_USER" "$CONFIG_DIR/config.yaml"
    chmod 600 "$CONFIG_DIR/config.yaml"
}

# Install binary
install_binary() {
    log_info "Installing binary to /usr/local/bin..."
    cp "./build/omnicatalog" /usr/local/bin/omnicatalog
    chmod 755 /usr/local/bin/omnicatalog
}

# Install systemd service
install_service() {
    log_info "Installing systemd service..."
    
    cat > /etc/systemd/system/omnicatalog.service <<EOF
[Unit]
Description=OmniRoute Model Catalog Validator for OpenCode
After=network-online.target omniroute.service
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
WorkingDirectory=$(pwd)
ExecStart=/usr/local/bin/omnicatalog daemon --config $CONFIG_DIR/config.yaml
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=omnicatalog

# Resource limits (minimum RAM)
MemoryLimit=256M
MemorySwapMax=0
CPUQuota=50%

# Security
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=$CONFIG_DIR $DATA_DIR $(dirname "$LOG_FILE")

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable omnicatalog
}

# Start service
start_service() {
    log_info "Starting omnicatalog service..."
    systemctl start omnicatalog
    sleep 2
    if systemctl is-active --quiet omnicatalog; then
        log_success "Service started successfully"
        systemctl status omnicatalog --no-pager
    else
        log_error "Service failed to start"
        systemctl status omnicatalog --no-pager
        journalctl -u omnicatalog -n 20 --no-pager
        exit 1
    fi
}

# Post-install instructions
post_install() {
    echo
    log_success "=== Installation Complete ==="
    echo
    log_info "Next steps:"
    echo "  1. Verify OmniRoute is running at: $OMNIROUTE_URL"
    echo "  2. If API key not set, configure it:"
    echo "     export OMNIROUTE_API_KEY='your-key'"
    echo "     or create file: $CONFIG_DIR/api_key (chmod 600)"
    echo "  3. Run initial validation:"
    echo "     omnicatalog run --config $CONFIG_DIR/config.yaml"
    echo "  4. Check status:"
    echo "     omnicatalog status --config $CONFIG_DIR/config.yaml"
    echo "  4. View logs:"
    echo "     journalctl -u omnicatalog -f"
    echo "     tail -f $LOG_FILE"
    echo
    log_info "Config file: $CONFIG_DIR/config.yaml"
    log_info "Database: $DATA_DIR/catalog.db"
    log_info "Log file: $LOG_FILE"
    log_info "Service: systemctl status omnicatalog"
}

# Main
main() {
    log_info "OmniCatalog Installer"
    check_root
    check_binary
    prompt_config
    create_directories
    generate_config
    install_binary
    install_service
    start_service
    post_install
}

main "$@"