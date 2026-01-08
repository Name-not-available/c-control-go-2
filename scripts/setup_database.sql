-- ============================================
-- Database Setup Script for Restaurant Bot
-- ============================================
-- 
-- This script creates the required PostgreSQL user and schema
-- for the Restaurant Finder Bot application.
--
-- Run this script as a PostgreSQL superuser (e.g., postgres)
-- 
-- Usage:
--   psql -U postgres -f setup_database.sql
--
-- Or connect to your database and run:
--   \i setup_database.sql
--
-- ============================================

-- Configuration Variables (modify these as needed)
-- These should match your .env file settings
\set db_name 'restaurant_bot'
\set db_user 'restaurant_bot_user'
\set db_password 'your_secure_password_here'
\set db_schema 'restaurant_bot'

-- ============================================
-- Step 1: Create Database (if it doesn't exist)
-- ============================================
-- Note: You cannot use variables in CREATE DATABASE in psql
-- You may need to create the database manually first:
--   CREATE DATABASE restaurant_bot;

SELECT 'Creating database if not exists...' AS status;

-- Check if database exists, create if not
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_database WHERE datname = 'restaurant_bot') THEN
        CREATE DATABASE restaurant_bot;
    END IF;
END
$$;

-- ============================================
-- Step 2: Create User (if it doesn't exist)
-- ============================================
SELECT 'Creating user if not exists...' AS status;

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'restaurant_bot_user') THEN
        CREATE USER restaurant_bot_user WITH PASSWORD 'your_secure_password_here';
    ELSE
        -- Update password if user exists
        ALTER USER restaurant_bot_user WITH PASSWORD 'your_secure_password_here';
    END IF;
END
$$;

-- ============================================
-- Step 3: Connect to the database
-- ============================================
\c restaurant_bot

-- ============================================
-- Step 4: Create Schema
-- ============================================
SELECT 'Creating schema...' AS status;

CREATE SCHEMA IF NOT EXISTS restaurant_bot;

-- ============================================
-- Step 5: Grant Permissions
-- ============================================
SELECT 'Granting permissions...' AS status;

-- Grant connect permission on database
GRANT CONNECT ON DATABASE restaurant_bot TO restaurant_bot_user;

-- Grant usage on schema
GRANT USAGE ON SCHEMA restaurant_bot TO restaurant_bot_user;

-- Grant all privileges on all tables in schema (current and future)
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA restaurant_bot TO restaurant_bot_user;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA restaurant_bot TO restaurant_bot_user;

-- Grant default privileges for future tables
ALTER DEFAULT PRIVILEGES IN SCHEMA restaurant_bot
    GRANT ALL PRIVILEGES ON TABLES TO restaurant_bot_user;

ALTER DEFAULT PRIVILEGES IN SCHEMA restaurant_bot
    GRANT ALL PRIVILEGES ON SEQUENCES TO restaurant_bot_user;

-- Set search path for the user
ALTER USER restaurant_bot_user SET search_path TO restaurant_bot, public;

-- ============================================
-- Step 6: Verify Setup
-- ============================================
SELECT 'Verifying setup...' AS status;

SELECT 
    'Database: ' || current_database() AS info
UNION ALL
SELECT 
    'Schema exists: ' || CASE WHEN EXISTS (
        SELECT 1 FROM information_schema.schemata WHERE schema_name = 'restaurant_bot'
    ) THEN 'Yes' ELSE 'No' END
UNION ALL
SELECT 
    'User exists: ' || CASE WHEN EXISTS (
        SELECT 1 FROM pg_roles WHERE rolname = 'restaurant_bot_user'
    ) THEN 'Yes' ELSE 'No' END;

SELECT '============================================' AS separator;
SELECT 'Setup completed successfully!' AS status;
SELECT '' AS blank;
SELECT 'Next steps:' AS info;
SELECT '1. Update your .env file with:' AS step1;
SELECT '   DB_HOST=localhost' AS config1;
SELECT '   DB_PORT=5432' AS config2;
SELECT '   DB_NAME=restaurant_bot' AS config3;
SELECT '   DB_USER=restaurant_bot_user' AS config4;
SELECT '   DB_PASSWORD=your_secure_password_here' AS config5;
SELECT '   DB_SCHEMA=restaurant_bot' AS config6;
SELECT '   DB_SSLMODE=prefer' AS config7;
SELECT '' AS blank2;
SELECT '2. Start the application - migrations will run automatically' AS step2;
SELECT '============================================' AS separator2;
