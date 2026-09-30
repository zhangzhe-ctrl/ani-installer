#!/usr/bin/env python3
"""Fedora lab wrapper: reuse .10's existing installer flock for one command.

No Fedora lock is created. EOF, SSH loss, or the bounded timeout releases the
node lock. The node password stays in the task's private file.
"""
import argparse
import selectors
import subprocess
import time


def run(password_file, command):
    remote = ("sudo -n sh -ec 'test -f /var/lib/ani-installer/ani-install.lock; "
              "exec timeout 7200 flock -n /var/lib/ani-installer/ani-install.lock "
              "sh -c \"echo ANI_EXISTING_LOCK_READY; cat >/dev/null\"'")
    holder = subprocess.Popen(['sshpass', '-f', str(password_file), 'ssh',
        '-o', 'ConnectTimeout=10', '-o', 'ServerAliveInterval=15',
        '-o', 'ServerAliveCountMax=2', 'ubuntu@172.16.101.10', remote],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE)
    child = None
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(holder.stdout, selectors.EVENT_READ)
            if not selector.select(timeout=20) or holder.stdout.readline().strip() != b'ANI_EXISTING_LOCK_READY':
                raise RuntimeError('existing node installation lock unavailable; no command started')
        print('Existing .10 installation lock acquired for this command', flush=True)
        child = subprocess.Popen(command)
        deadline = time.monotonic() + 7000
        while child.poll() is None:
            if holder.poll() is not None or time.monotonic() >= deadline:
                child.terminate()
                try: child.wait(timeout=15)
                except subprocess.TimeoutExpired: child.kill(); child.wait()
                raise RuntimeError('node lock lost or command deadline exceeded; command stopped')
            time.sleep(1)
        return child.returncode
    finally:
        if child is not None and child.poll() is None:
            child.terminate()
            child.wait(timeout=15)
        holder.stdin.close()
        try: holder.wait(timeout=20)
        except subprocess.TimeoutExpired: holder.terminate(); holder.wait(timeout=10)
        print('Existing node lock released', flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--password-file', required=True)
    parser.add_argument('command', nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command[:1] == ['--'] else args.command
    if not command: parser.error('a command is required')
    raise SystemExit(run(args.password_file, command))
