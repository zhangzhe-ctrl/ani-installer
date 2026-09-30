#!/usr/bin/env python3
"""Prepare this task's scoped SQL from committed ANI and actual KB-image SQL.

Run only on Fedora. This prepares files; the shared application entry executes
them against the dedicated empty database after identity/ownership checks.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re

TASK = 'ani-system-fast-20260930'
DB = 'ani_fast_20260930'
ROLES = {'ani_app': 'ani_fast_app', 'ani_app_user': 'ani_fast_app_user',
         'ani_migrator': 'ani_fast_migrator', 'ani_outbox_publisher': 'ani_fast_outbox_publisher',
         'ani_metering_user': 'ani_fast_metering_user'}


def literal(value):
    return "'" + str(value).replace("'", "''") + "'"


def adapt(name, text):
    # Instance roles are initialized separately; never execute legacy role mutation.
    if name == '20260501000100_init_schema.sql':
        marker = '-- SECTION 1: TENANTS'
        if marker not in text: raise ValueError('initial schema boundary changed')
        text = 'CREATE SCHEMA IF NOT EXISTS public;\n' + text[text.index(marker):]
    elif name == '20260731000100_metering_usage.sql':
        marker = '-- 1) 建表'
        if marker not in text: raise ValueError('metering schema boundary changed')
        text = text[text.index(marker):]
    elif name == '20260828000100_database_roles_hardening.sql':
        text = '-- Task-owned roles and memberships were established in bootstrap.sql.\nSELECT 1;\n'
    for old, new in ROLES.items():
        text = re.sub(r'\b' + old + r'\b', new, text)
    text = re.sub(r'(FOR ROLE) ani\b', r'\1 ani_fast_migrator', text)
    text = re.sub(r'(ON DATABASE) ani\b', r'\1 ' + DB, text)
    # ani_metering_writer is fixed in the downloaded binary. Bootstrap refuses
    # an existing unowned role; all its new grants are confined to this database.
    active = '\n'.join(line for line in text.splitlines() if not line.lstrip().startswith('--'))
    if re.search(r'\b(?:CREATE|ALTER) ROLE\b|\b(?:BEGIN|COMMIT)\s*;', active, re.I):
        raise ValueError('unexpected instance role mutation or transaction in ' + name)
    if re.search(r'\b(?:ani_app|ani_app_user|ani_migrator|ani_outbox_publisher|ani_metering_user)\b', active):
        raise ValueError('shared legacy role left in ' + name)
    return text


def prepare(shared, kb, runtime, output):
    output.mkdir(mode=0o700, exist_ok=False)
    passwords = runtime['database_passwords']
    definitions = {
        'ani_fast_app': 'NOLOGIN NOBYPASSRLS',
        'ani_fast_app_user': 'LOGIN NOBYPASSRLS',
        'ani_fast_gateway_user': 'LOGIN NOBYPASSRLS',
        'ani_fast_metering_user': 'LOGIN NOBYPASSRLS',
        'ani_fast_migrator': 'NOLOGIN NOBYPASSRLS',
        'ani_fast_outbox_publisher': 'NOLOGIN BYPASSRLS',
        'ani_metering_writer': 'NOLOGIN BYPASSRLS',
    }
    bootstrap = []
    for role, flags in definitions.items():
        password = (' PASSWORD ' + literal(passwords[role])) if flags.startswith('LOGIN ') else ''
        bootstrap.append(f"""DO $guard$ BEGIN
IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname={literal(role)}) THEN
 IF COALESCE(shobj_description((SELECT oid FROM pg_roles WHERE rolname={literal(role)}),'pg_authid'),'') <> {literal(TASK)} THEN
  RAISE EXCEPTION 'role ownership conflict: {role}';
 END IF;
ELSE
 CREATE ROLE {role} {flags} NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT{password};
 COMMENT ON ROLE {role} IS {literal(TASK)};
END IF;
END $guard$;""")
    bootstrap.append(f"""DO $guard$ BEGIN
IF EXISTS (SELECT 1 FROM pg_database WHERE datname='{DB}' AND
 (pg_get_userbyid(datdba)<>'ani_fast_migrator' OR COALESCE(shobj_description(oid,'pg_database'),'')<>{literal(TASK)})) THEN
 RAISE EXCEPTION 'database ownership conflict: {DB}';
END IF;
END $guard$;
SELECT 'CREATE DATABASE {DB} OWNER ani_fast_migrator' WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname='{DB}')\gexec
COMMENT ON DATABASE {DB} IS {literal(TASK)};
GRANT ani_fast_app TO ani_fast_app_user, ani_fast_gateway_user;
GRANT ani_metering_writer TO ani_fast_metering_user, ani_fast_gateway_user;
GRANT CONNECT ON DATABASE {DB} TO ani_fast_app_user, ani_fast_gateway_user, ani_fast_metering_user;
""")
    (output / 'bootstrap.sql').write_text('\n'.join(bootstrap))
    (output / 'ledger.sql').write_text("""SET ROLE ani_fast_migrator;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE TABLE IF NOT EXISTS ani_fast_migrations (file TEXT PRIMARY KEY, sha256 TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());
RESET ROLE;
""")
    files = sorted(shared.glob('*.sql')) + sorted(kb.glob('*.sql'))
    if not files or files[0].name != '20260501000100_init_schema.sql' or len(list(kb.glob('*.sql'))) != 9:
        raise ValueError('expected committed shared schema and the nine actual KB migrations')
    inventory, evidence = [], []
    for index, path in enumerate(files, 1):
        raw = path.read_text();body = adapt(path.name, raw)
        name = f'{index:03d}_' + path.name
        digest = hashlib.sha256(body.encode()).hexdigest()
        wrapper = ('BEGIN;\nSET LOCAL ROLE ani_fast_migrator;\n' + body + '\nRESET ROLE;\n' +
                   f"INSERT INTO ani_fast_migrations(file,sha256) VALUES({literal(name)},{literal(digest)});\nCOMMIT;\n")
        (output / name).write_text(wrapper)
        inventory.append(name + '\t' + digest)
        evidence.append({'file': name, 'sourceFile': path.name, 'sourceSha256': hashlib.sha256(raw.encode()).hexdigest(),
                         'adaptedBodySha256': digest, 'sourceKind': 'actual-kb-image' if path.parent == kb else 'committed-ani'})
    admin = runtime['admin']
    if not re.fullmatch(r'[A-Za-z0-9_.-]{1,64}', admin['username']) or not re.fullmatch(r'\$2[aby]\$12\$.{53}', admin['password_hash']):
        raise ValueError('admin username and bcrypt cost-12 hash are required')
    username = literal('local:' + admin['username'])
    seed = f"""BEGIN;
DO $guard$ BEGIN
IF EXISTS (SELECT 1 FROM users WHERE username={username} AND tenant_id IS NULL) THEN
 IF NOT EXISTS (SELECT 1 FROM users u JOIN user_roles ur ON ur.user_id=u.id JOIN roles r ON r.id=ur.role_id
 WHERE u.username={username} AND u.tenant_id IS NULL AND r.tenant_id IS NULL AND r.name='platform-admin') THEN
  RAISE EXCEPTION 'existing first-admin user has incompatible role; no reset';
 END IF;
ELSE
 INSERT INTO users(id,tenant_id,username,email,display_name,password_hash,status,is_deleted)
 VALUES(gen_random_uuid(),NULL,{username},{literal(admin['email'])},{literal(admin['username'])},{literal(admin['password_hash'])},'active',false);
 INSERT INTO user_roles(user_id,role_id) SELECT u.id,r.id FROM users u CROSS JOIN roles r
 WHERE u.username={username} AND u.tenant_id IS NULL AND r.tenant_id IS NULL AND r.name='platform-admin';
END IF;
END $guard$;
COMMIT;
"""
    name = '999_first_admin.sql';digest = hashlib.sha256(seed.encode()).hexdigest()
    (output / name).write_text(seed.replace('COMMIT;', f"INSERT INTO ani_fast_migrations(file,sha256) VALUES({literal(name)},{literal(digest)});\nCOMMIT;"))
    inventory.append(name + '\t' + digest)
    (output / 'migrations.tsv').write_text('\n'.join(inventory) + '\n')
    (output / 'source-identities.json').write_text(json.dumps(evidence, indent=2) + '\n')
    print('Prepared scoped SQL files; execution remains not_run:', len(inventory))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('shared', 'kb', 'runtime', 'output'): parser.add_argument('--' + name, type=Path, required=True)
    args = parser.parse_args()
    prepare(args.shared, args.kb, json.loads(args.runtime.read_text()), args.output)
