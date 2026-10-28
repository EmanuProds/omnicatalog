#!/bin/bash
# OmniCatalog Uninstaller
# Removes OmniCatalog systemd service, binary, and optionally config/data

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $*"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }

SERVICE_NAME="omnicatalog"
BINARY_PATH="/usr/local/bin/omnicatalog"
SERVICE_FILE="/etc/systemd/system/omnicatalog.service"

check_root() {
    if [[ $EUID -ne 0 ]]; then
        log_error "This script must be run as root (use sudo)"
        exit 1
    fi
}

confirm_removal() {
    echo
    log_warn "This will remove:"
    echo "  - systemd service: $SERVICE_NAME"
    echo "  - Binary: $BINARY_PATH"
    echo "  - Service file: $SERVICE_FILE"
    echo
    read -p "Also remove config (~/.config/omnicatalog) and data (~/.local/share/omnicatalog)? [y/N]: " REMOVE_DATA
    REMOVE_DATA=${REMOVE_DATA:-N}

    echo
    read -p "Are you sure you want to uninstall OmniCatalog? [y/N]: " CONFIRM
    CONFIRM=${CONFIRM:-N}
    if [[ ! $CONFIRM =~ ^[Yy]$ ]]; then
        log_info "Uninstall cancelled"
        exit 0
    fi
}

stop_service() {
    log_info "Stopping service..."
    systemctl stop omnicatalog 2>/dev/null || true
    systemctl disable omnicatalog 2>/dev/null || true
}

remove_service() {
    log_info "Removing systemd service..."
    systemctl stop omnicatalog 2>/dev/null || true
    systemctl disable omnicatalog 2>/dev/null || true
    rm -f /etc/systemd/system/omnicatalog.service
    systemctl daemon-reload
    log_success "Systemd service removed"
}

remove_binary() {
    log_info "Removing binary..."
    rm -f /usr/local/bin/omnicatalog
    log_success "Binary removed"
}

remove_data() {
    if [[ $REMOVE_DATA =~ ^[Yy]$ ]]; then
        log_info "Removing config and data directories..."
        # Get the actual user who ran sudo
        TARGET_USER="${SUDO_USER:-$USER}"
        USER_HOME=$(eval echo "~$TARGET_USER")

        CONFIG_DIR="$USER_HOME/.config/omnicatalog"
        DATA_DIR="$USER_HOME/.local/share/omnicatalog"
        LOG_FILE="/var/log/omnicatalog.log"

        rm -rf "$CONFIG_DIR" 2>/dev/null || true
        rm -rf "$DATA_DIR" 2>/dev/null || true
        rm -f "$LOG_FILE" 2>/dev/null || true
        rm -f "$LOG_FILE."* 2>/dev/null || true

        log_success "Config and data removed"
    else
        log_info "Keeping config and data directories"
    fi
}

verify_removal() {
    log_info "Verifying removal..."

    local issues=0

    if [[ -f /usr/local/bin/omnicatalog ]]; then
        log_warn "Binary still exists: /usr/local/bin/omnicatalog"
        ((issues++))
    fi

    if [[ -f /etc/systemd/system/omnicatalog.service ]]; then
        log_warn "Service file still exists: /etc/systemd/system/omnicatalog.service"
        ((issues++))
    fi

    if systemctl is-enabled omnicatalog &>/dev/null; then
        log_warn "Service still enabled"
        ((issues++))
    fi

    if systemctl is-active omnicatalog &>/dev/null; then
        log_warn "Service still active"
        ((issues++))
    fi

    if [[ $issues -eq 0 ]]; then
        log_success "All components removed successfully"
    else
        log_warn "Found $issues potential issues"
    fi
}

main() {
    echo "OmniCatalog Uninstaller"
    check_root
    confirm_removal
    stop_service
    remove_service
    remove_binary
    remove_data
    verify_removal
    echo
    log_success "Uninstall complete"
}

main "$@"