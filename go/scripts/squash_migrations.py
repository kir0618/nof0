#!/usr/bin/env python3
"""
Migration Squasher - Simple Python Version

Consolidates multiple migration files into a single initial schema.
Uses pg_dump to get the current database state.

Usage:
    export POSTGRES_DSN="postgres://user:pass@localhost:5432/dbname?sslmode=disable"
    python scripts/squash_migrations.py [--dry-run]
"""

import argparse
import os
import re
import shutil
import subprocess
import sys
from datetime import datetime
from pathlib import Path


# Colors
RED = '\033[0;31m'
GREEN = '\033[0;32m'
YELLOW = '\033[1;33m'
RESET = '\033[0m'

MIGRATIONS_DIR = Path('migrations')
BACKUP_DIR = Path('migrations_backup')


def log(message, level='INFO', dry_run=False):
    """Print formatted log message."""
    prefix = f"{YELLOW}[DRY-RUN]{RESET} " if dry_run else ""
    color = GREEN if level == 'INFO' else RED
    print(f"{prefix}{color}[{level}]{RESET} {message}")


def run_command(cmd, capture=True):
    """Run shell command and return output."""
    result = subprocess.run(
        cmd,
        shell=isinstance(cmd, str),
        capture_output=capture,
        text=True
    )
    if result.returncode != 0:
        raise RuntimeError(f"Command failed: {result.stderr}")
    return result.stdout if capture else None


def get_pg_connection():
    """Get PostgreSQL connection string from environment."""
    dsn = os.getenv('POSTGRES_DSN')
    if not dsn:
        log("POSTGRES_DSN environment variable is not set", 'ERROR')
        print("Example: export POSTGRES_DSN='postgres://user:pass@localhost:5432/dbname?sslmode=disable'")
        sys.exit(1)
    # Convert golang-migrate format to standard PostgreSQL
    return dsn.replace('postgres://', 'postgresql://')


def test_connection(pg_conn, dry_run):
    """Test database connection."""
    log("Testing database connection...", dry_run=dry_run)
    try:
        run_command(f'psql "{pg_conn}" -c "SELECT 1"')
        log("✓ Database connection successful", dry_run=dry_run)
    except RuntimeError:
        log("Cannot connect to database", 'ERROR')
        sys.exit(1)


def backup_migrations(dry_run):
    """Backup existing migration files."""
    if dry_run:
        log(f"Would backup migrations to {BACKUP_DIR}", dry_run=dry_run)
        return

    log(f"Backing up existing migrations to {BACKUP_DIR}...", dry_run=dry_run)
    BACKUP_DIR.mkdir(parents=True, exist_ok=True)

    sql_files = list(MIGRATIONS_DIR.glob('*.sql'))
    for file in sql_files:
        shutil.copy2(file, BACKUP_DIR / file.name)

    log("✓ Backup complete", dry_run=dry_run)


def dump_schema(pg_conn, dry_run):
    """Dump current database schema using pg_dump."""
    log("Dumping current database schema...", dry_run=dry_run)

    cmd = [
        'pg_dump', pg_conn,
        '--schema-only',
        '--no-owner',
        '--no-privileges',
        '--no-tablespaces',
        '--no-security-labels',
        '--no-comments',
    ]

    try:
        output = subprocess.run(cmd, capture_output=True, text=True, check=True)
        log("✓ Schema dumped successfully", dry_run=dry_run)
        return output.stdout
    except subprocess.CalledProcessError as e:
        log(f"pg_dump failed: {e.stderr}", 'ERROR')
        sys.exit(1)


def clean_dump(dump):
    """Clean up the pg_dump output."""
    lines = []
    skip_patterns = [
        r'^SET ',
        r'^SELECT pg_catalog\.set_config',
        r'schema_migrations',
        r'^--',
    ]

    for line in dump.split('\n'):
        # Skip unwanted lines
        if any(re.match(pattern, line) for pattern in skip_patterns):
            continue
        lines.append(line)

    # Remove consecutive empty lines
    cleaned = []
    prev_empty = False
    for line in lines:
        is_empty = not line.strip()
        if is_empty and prev_empty:
            continue
        cleaned.append(line)
        prev_empty = is_empty

    return '\n'.join(cleaned)


def generate_up_migration(schema):
    """Generate the .up.sql migration."""
    header = f"""-- Consolidated initial schema for NOF0
-- Generated: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}
-- This migration consolidates all previous migrations into a single baseline
--
-- To regenerate: python scripts/squash_migrations.py

"""
    return header + schema


def generate_down_migration():
    """Generate the .down.sql migration."""
    return """-- Rollback consolidated initial schema
--
-- This drops all objects in the public schema

DO $$
DECLARE
    r RECORD;
BEGIN
    -- Drop all materialized views
    FOR r IN (SELECT matviewname FROM pg_matviews WHERE schemaname = 'public')
    LOOP
        EXECUTE 'DROP MATERIALIZED VIEW IF EXISTS ' || quote_ident(r.matviewname) || ' CASCADE';
    END LOOP;

    -- Drop all views
    FOR r IN (SELECT viewname FROM pg_views WHERE schemaname = 'public')
    LOOP
        EXECUTE 'DROP VIEW IF EXISTS ' || quote_ident(r.viewname) || ' CASCADE';
    END LOOP;

    -- Drop all tables
    FOR r IN (SELECT tablename FROM pg_tables WHERE schemaname = 'public')
    LOOP
        EXECUTE 'DROP TABLE IF EXISTS ' || quote_ident(r.tablename) || ' CASCADE';
    END LOOP;

    -- Drop all functions
    FOR r IN (SELECT proname, oidvectortypes(proargtypes) as argtypes
              FROM pg_proc INNER JOIN pg_namespace ON pg_proc.pronamespace = pg_namespace.oid
              WHERE pg_namespace.nspname = 'public')
    LOOP
        EXECUTE 'DROP FUNCTION IF EXISTS ' || quote_ident(r.proname) || '(' || r.argtypes || ') CASCADE';
    END LOOP;
END $$;
"""


def write_migrations(up_content, down_content, dry_run):
    """Write new migration files."""
    up_file = MIGRATIONS_DIR / '001_initial_schema.up.sql'
    down_file = MIGRATIONS_DIR / '001_initial_schema.down.sql'

    if dry_run:
        log(f"Would remove old migration files from {MIGRATIONS_DIR}", dry_run=dry_run)
        log(f"Would write {len(up_content)} bytes to {up_file}", dry_run=dry_run)
        log(f"Would write {len(down_content)} bytes to {down_file}", dry_run=dry_run)

        print("\n--- Preview of 001_initial_schema.up.sql (first 30 lines) ---")
        print('\n'.join(up_content.split('\n')[:30]))
        return

    # Remove old migrations
    log("Removing old migration files...", dry_run=dry_run)
    for file in MIGRATIONS_DIR.glob('*.sql'):
        file.unlink()

    # Write new migrations
    log("Writing new migration files...", dry_run=dry_run)
    up_file.write_text(up_content)
    down_file.write_text(down_content)

    log(f"✓ Created {up_file}", dry_run=dry_run)
    log(f"✓ Created {down_file}", dry_run=dry_run)


def print_next_steps():
    """Print next steps for user."""
    print("\n" + "=" * 60)
    print("NEXT STEPS:")
    print("=" * 60)
    print("1. Review the new migration file:")
    print("   less migrations/001_initial_schema.up.sql")
    print("")
    print("2. Reset your database:")
    print("   dropdb <dbname> && createdb <dbname>")
    print("")
    print("3. Apply the new migration:")
    print("   make migrate-up")
    print("")
    print("4. Verify schema:")
    print("   psql \"$POSTGRES_DSN\" -c '\\dt'")
    print("")
    print(f"5. Old migrations backed up at:")
    print(f"   {BACKUP_DIR}/")
    print("=" * 60)


def main():
    parser = argparse.ArgumentParser(
        description='Squash multiple migrations into a single initial schema',
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__
    )
    parser.add_argument('--dry-run', action='store_true',
                        help='Show what would be done without making changes')

    args = parser.parse_args()
    dry_run = args.dry_run

    log("=== Starting Migration Squash ===", dry_run=dry_run)

    # Get connection and test it
    pg_conn = get_pg_connection()
    test_connection(pg_conn, dry_run)

    # Backup old migrations
    backup_migrations(dry_run)

    # Dump and clean schema
    raw_dump = dump_schema(pg_conn, dry_run)
    cleaned_dump = clean_dump(raw_dump)

    # Generate migrations
    up_content = generate_up_migration(cleaned_dump)
    down_content = generate_down_migration()

    # Write new migrations
    write_migrations(up_content, down_content, dry_run)

    log("=== Migration Squash Complete ===", dry_run=dry_run)

    if not dry_run:
        print_next_steps()


if __name__ == '__main__':
    main()
