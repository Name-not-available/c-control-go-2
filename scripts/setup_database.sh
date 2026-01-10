#!/bin/bash

# ============================================
# Database Setup Script for Restaurant Bot
# ============================================
#
# This script creates the required PostgreSQL database, user, and schema
# for the Restaurant Finder Bot application.
#
# Usage:
#   ./setup_database.sh [OPTIONS]
#
# Options:
#   -h, --host       PostgreSQL host (default: localhost)
#   -p, --port       PostgreSQL port (default: 5432)
#   -U, --superuser  PostgreSQL superuser (default: postgres)
#   -d, --dbname     Database name to create (default: restaurant_bot)
#   -u, --user       Application user to create (default: restaurant_bot_user)
#   -s, --schema     Schema name to create (default: restaurant_bot)
#   -P, --password   Password for the application user (will prompt if not provided)
#   --help           Show this help message
#
# ============================================

set -e

# Default values
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_SUPERUSER="${DB_SUPERUSER:-postgres}"
DB_NAME="${DB_NAME:-restaurant_bot}"
DB_USER="${DB_USER:-restaurant_bot_user}"
DB_SCHEMA="${DB_SCHEMA:-restaurant_bot}"
DB_PASSWORD=""

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Print colored message
print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Show help
show_help() {
    echo "Database Setup Script for Restaurant Bot"
    echo ""
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  -h, --host       PostgreSQL host (default: localhost)"
    echo "  -p, --port       PostgreSQL port (default: 5432)"
    echo "  -U, --superuser  PostgreSQL superuser (default: postgres)"
    echo "  -d, --dbname     Database name to create (default: restaurant_bot)"
    echo "  -u, --user       Application user to create (default: restaurant_bot_user)"
    echo "  -s, --schema     Schema name to create (default: restaurant_bot)"
    echo "  -P, --password   Password for the application user"
    echo "  --help           Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0"
    echo "  $0 -h db.example.com -P mypassword"
    echo "  $0 --dbname myapp --user myapp_user --schema myapp"
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--host)
            DB_HOST="$2"
            shift 2
            ;;
        -p|--port)
            DB_PORT="$2"
            shift 2
            ;;
        -U|--superuser)
            DB_SUPERUSER="$2"
            shift 2
            ;;
        -d|--dbname)
            DB_NAME="$2"
            shift 2
            ;;
        -u|--user)
            DB_USER="$2"
            shift 2
            ;;
        -s|--schema)
            DB_SCHEMA="$2"
            shift 2
            ;;
        -P|--password)
            DB_PASSWORD="$2"
            shift 2
            ;;
        --help)
            show_help
            exit 0
            ;;
        *)
            print_error "Unknown option: $1"
            show_help
            exit 1
            ;;
    esac
done

# Prompt for password if not provided
if [ -z "$DB_PASSWORD" ]; then
    echo -n "Enter password for user '$DB_USER': "
    read -s DB_PASSWORD
    echo ""
    
    if [ -z "$DB_PASSWORD" ]; then
        print_error "Password cannot be empty"
        exit 1
    fi
    
    echo -n "Confirm password: "
    read -s DB_PASSWORD_CONFIRM
    echo ""
    
    if [ "$DB_PASSWORD" != "$DB_PASSWORD_CONFIRM" ]; then
        print_error "Passwords do not match"
        exit 1
    fi
fi

echo "============================================"
echo " Restaurant Bot Database Setup"
echo "============================================"
echo ""
print_info "Configuration:"
echo "  Host:      $DB_HOST"
echo "  Port:      $DB_PORT"
echo "  Superuser: $DB_SUPERUSER"
echo "  Database:  $DB_NAME"
echo "  User:      $DB_USER"
echo "  Schema:    $DB_SCHEMA"
echo ""

# Check if psql is available
if ! command -v psql &> /dev/null; then
    print_error "psql command not found. Please install PostgreSQL client."
    exit 1
fi

# Test connection
print_info "Testing connection to PostgreSQL..."
if ! psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_SUPERUSER" -c "SELECT 1" postgres &> /dev/null; then
    print_error "Failed to connect to PostgreSQL as $DB_SUPERUSER"
    print_info "Make sure PostgreSQL is running and you have the correct credentials"
    exit 1
fi
print_success "Connection successful"

# Create database
print_info "Creating database '$DB_NAME'..."
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_SUPERUSER" postgres <<EOF
SELECT 'Creating database...' AS status;
DO \$\$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_database WHERE datname = '$DB_NAME') THEN
        CREATE DATABASE $DB_NAME;
        RAISE NOTICE 'Database created';
    ELSE
        RAISE NOTICE 'Database already exists';
    END IF;
END
\$\$;
EOF
print_success "Database ready"

# Create user
print_info "Creating user '$DB_USER'..."
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_SUPERUSER" postgres <<EOF
DO \$\$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '$DB_USER') THEN
        CREATE USER $DB_USER WITH PASSWORD '$DB_PASSWORD';
        RAISE NOTICE 'User created';
    ELSE
        ALTER USER $DB_USER WITH PASSWORD '$DB_PASSWORD';
        RAISE NOTICE 'User password updated';
    END IF;
END
\$\$;
EOF
print_success "User ready"

# Create schema and grant permissions
print_info "Creating schema '$DB_SCHEMA' and granting permissions..."
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_SUPERUSER" "$DB_NAME" <<EOF
-- Create schema
CREATE SCHEMA IF NOT EXISTS $DB_SCHEMA;

-- Grant connect permission on database
GRANT CONNECT ON DATABASE $DB_NAME TO $DB_USER;

-- Grant usage on schema
GRANT USAGE ON SCHEMA $DB_SCHEMA TO $DB_USER;
GRANT CREATE ON SCHEMA $DB_SCHEMA TO $DB_USER;

-- Grant all privileges on all tables in schema (current and future)
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA $DB_SCHEMA TO $DB_USER;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA $DB_SCHEMA TO $DB_USER;

-- Grant default privileges for future tables
ALTER DEFAULT PRIVILEGES IN SCHEMA $DB_SCHEMA
    GRANT ALL PRIVILEGES ON TABLES TO $DB_USER;

ALTER DEFAULT PRIVILEGES IN SCHEMA $DB_SCHEMA
    GRANT ALL PRIVILEGES ON SEQUENCES TO $DB_USER;

-- Set search path for the user
ALTER USER $DB_USER SET search_path TO $DB_SCHEMA, public;
EOF
print_success "Schema and permissions configured"

# Verify setup
print_info "Verifying setup..."
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_SUPERUSER" "$DB_NAME" -t <<EOF
SELECT 'Database: ' || current_database();
SELECT 'Schema exists: ' || CASE WHEN EXISTS (
    SELECT 1 FROM information_schema.schemata WHERE schema_name = '$DB_SCHEMA'
) THEN 'Yes' ELSE 'No' END;
SELECT 'User exists: ' || CASE WHEN EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname = '$DB_USER'
) THEN 'Yes' ELSE 'No' END;
EOF

echo ""
echo "============================================"
print_success "Database setup completed successfully!"
echo "============================================"
echo ""
echo "Add the following to your .env file:"
echo ""
echo "  DB_HOST=$DB_HOST"
echo "  DB_PORT=$DB_PORT"
echo "  DB_NAME=$DB_NAME"
echo "  DB_USER=$DB_USER"
echo "  DB_PASSWORD=$DB_PASSWORD"
echo "  DB_SCHEMA=$DB_SCHEMA"
echo "  DB_SSLMODE=prefer"
echo "  DB_ALLOW_INSECURE_SSL=false"
echo ""
echo "Then start the application - migrations will run automatically."
echo "============================================"
