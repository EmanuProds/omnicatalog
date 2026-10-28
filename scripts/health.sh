#!/bin/bash
# OmniCatalog Health Check
# Verifies OmniRoute connectivity, database, and configuration

set -euo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $*"; }
log_success() { echo -e "${GREEN}[OK]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[FAIL]${NC} $*"; }

CONFIG_FILE="${1:-~/.config/omnicatalog/config.yaml}"
CONFIG_FILE=$(eval echo "$CONFIG_FILE")

check_config() {
    log_info "Checking configuration..."
    if [[ ! -f "$CONFIG_FILE" ]]; then
        log_error "Config file not found: $CONFIG_FILE"
        return 1
    fi
    log_success "Config file exists: $CONFIG_FILE"
    return 0
}

check_omniroute() {
    log_info "Checking OmniRoute connectivity..."

    # Parse config for base_url and api_key
    local base_url=$(grep 'base_url:' "$CONFIG_FILE" | head -1 | sed 's/.*base_url: *["\?]*\([^" ]*\).*/\1/')
    local api_key=$(grep 'api_key:' "$CONFIG_FILE" | head -1 | sed 's/.*api_key: *["\?]*\([^" ]*\).*/\1/')

    # Resolve env var or file
    if [[ "$api_key" =~ ^\{env:(.+)\}$ ]]; local api_key="${!BASH_REMATCH[1]}"
    elif [[ "$api_key" =~ ^\{file:(.+)\}$ ]]; then api_key=$(cat "${BASH_REMATCH[1]}" 2>/dev/null | tr -d '[:space:]') || api_key=""

    if [[ -z "$base_url" ]]; then
        log_error "base_url not found in config"
        return 1
    fi

    log_info "Testing $base_url/api/providers..."
    if curl -s -f -H "Authorization: Bearer $api_key" "$base_url/api/providers" >/dev/null; then
        log_success "OmniRoute API reachable"
        return 0
    else
        log_error "OmniRoute API unreachable"
        return 1
    fi
}

check_database() {
    log_info "Checking database..."
    local db_path=$(grep 'path:' "$CONFIG_FILE" | grep database -A1 | tail -1 | sed 's/.*path: *["\?]*\([^" ]*\).*/\1/')
    db_path=$(eval echo "$db_path")

    if [[ ! -f "$db_path" ]]; then
        log_warn "Database not found: $db_path"
        return 1
    fi

    # Check if we can read it
    if sqlite3 "$db_path" "SELECT 1;" >/dev/null 2>&1; then
        log_success "Database accessible: $db_path"
        # Count models
        local model_count=$(sqlite3 "$db_path" "SELECT COUNT(*) FROM models WHERE free_confirmed=1;" 2>/dev/null || echo 0)
        log_info "Validated models in catalog: $model_count"
        return 0
    else
        log_error "Database not accessible: $db_path"
        return 1
    fi
}

check_binary() {
    log_info "Checking binary..."
    if command -v omnicatalog >/dev/null 2>&1; then
        local version=$(omnicatalog version 2>/dev/null | head -1)
        log_success "Binary found: $version"
        return 0
    else
        log_warn "omnicatalog not in PATH"
        return 1
    fi
}

check_systemd() {
    log_info "Checking systemd service..."
    if systemctl is-enabled omnicatalog >/dev/null 2>&1; then
        if systemctl is-active omnicatalog >/dev/null 2>&1; then
            log_success "Service enabled and running"
            return 0
        else
            log_warn "Service enabled but not running"
            return 1
        fi
    else
        log_warn "Service not enabled"
        return 1
    fi
}

main() {
    echo "OmniCatalog Health Check"
    echo "========================"
    echo

    local failed=0

    check_config || ((failed++))
    check_binary || ((failed++))
    check_omniroute || ((failed++))
    check_database || ((failed++))
    check_systemd || ((failed++))

    echo
    echo "========================"
    if [[ $failed -eq 0 ]]; then
        log_success "All checks passed!"
        exit 0
    else
        log_error "$failed check(s) failed"
        exit 1
    fi
}

main "$@"